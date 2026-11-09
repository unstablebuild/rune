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
	"context"
	"errors"
	stdimage "image"
	"log/slog"
	"net/url"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/component/template"
	tconfig "unstable.build/rune/internal/config"
	"unstable.build/rune/internal/extension/extutil"
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
	// SendMessageIcon marks each sent message from the transcript's left
	// padding, left of its first row. It is drawn one cell wide, and not
	// at all when zero or when the pane is too narrow to pad the
	// transcript.
	SendMessageIcon rune
	// PinnedMessageMaxRows is how many rows the newest user message
	// scrolled above the transcript's viewport may take when pinned over
	// its top. Longer messages are cut short with an ellipsis, and the pin
	// never takes more than half the transcript. Zero disables the pin.
	PinnedMessageMaxRows int
	// PinnedMessageHoverBackground replaces the pinned message's
	// background under the pointer, as clicking it scrolls back to the
	// message. ColorDefault defaults to silver.
	PinnedMessageHoverBackground term.Color
	// JumpToBottomAttr styles the arrow drawn at the bottom of the
	// transcript while it is scrolled up, and JumpToBottomHoverAttr styles
	// it under the pointer. Zero values default to silver and bold white.
	JumpToBottomAttr      term.Attributes
	JumpToBottomHoverAttr term.Attributes

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
	// used for received messages. A nil value uses DefaultMarkdownConfig().
	MarkdownConfig *markdown.Config

	// ReasoningMarkdownConfig, when non-nil, configures the markdown
	// renderer used for reasoning text. A nil value derives a config from
	// MarkdownConfig styled with ReasoningStringConfig's attributes.
	ReasoningMarkdownConfig *markdown.Config

	// Clipboard, when non-nil, enables MarkdownConfig.CodeBlockCopy for
	// transcript code blocks; clicking the icon copies the block's source
	// to the default register and swaps the icon for
	// MarkdownConfig.CodeBlockCopiedIcon until the pointer leaves it. The
	// icon is only drawn when MarkdownConfig.CodeBlockPadding has room for it.
	Clipboard clipboard.Register

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

	// OnLinkClick is called when a markdown link is clicked in the messages area.
	// Returning true marks the click as handled and suppresses text selection.
	// A nil callback ignores clicks.
	OnLinkClick func(*url.URL) bool
}

// DefaultPinnedMessageMaxRows keeps the pinned message to a glimpse of
// what was asked, as the reply it heads is what is being read.
const DefaultPinnedMessageMaxRows = 2

// DefaultComponentConfig is the chat a host gets when nothing configures
// it.
func DefaultComponentConfig() ComponentConfig {
	return ComponentConfig{
		MessagesRowConfig: component.SpanConfig{
			PadHorizontal:    -90,
			PadVertical:      2,
			ContentAlignment: component.AlignmentCentered,
		},
		InputRowColumns: 10,
		InputBox: InputBoxConfig{
			Placeholder: "Message your assistant...",
			PlaceholderConfig: component.StringResponsiveConfig{
				NoSplitWords: true,
				StringConfig: component.StringConfig{
					Alignment:            component.AlignmentLeft,
					Attributes:           term.Attributes{Fg: term.ColorGray},
					BackgroundAttributes: term.Attributes{},
				},
			},
			ContentAttr: term.Attributes{},
			FrameAttr:   term.Attributes{},
		},
		ReceiveMessageStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorSilver},
			BackgroundAttributes: term.Attributes{},
		},
		ReceiveMessageSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		ReasoningStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.GetColor("dimgray")},
			BackgroundAttributes: term.Attributes{},
		},
		ReasoningSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{},
			BackgroundAttributes: term.Attributes{},
		},
		SendMessageSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageBottomPad: 1,
		SendMessageIcon:      DefaultSendMessageIcon,
		PinnedMessageMaxRows: DefaultPinnedMessageMaxRows,
		ToolCallStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorPurple},
			BackgroundAttributes: term.Attributes{},
		},
		ToolCallSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		ToolCallArgsStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorGray},
			BackgroundAttributes: term.Attributes{},
		},
		ToolResultStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorGray},
			BackgroundAttributes: term.Attributes{},
		},
		ToolResultSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		ErrorStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorRed},
			BackgroundAttributes: term.Attributes{},
		},
		ErrorSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		WarningStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorYellow},
			BackgroundAttributes: term.Attributes{},
		},
		WarningSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		CommandOutputSpanConfig: component.SpanConfig{
			PadVertical: 1,
		},
		ToolResultMaxLines: 5,
		StartCollapsed:     true,
		ReasoningAnnotationStringConfig: component.StringConfig{
			Alignment:  component.AlignmentLeft,
			Attributes: term.Attributes{Attrs: term.AttrDim},
		},
		MarkdownConfig: DefaultMarkdownConfig(),
		PromptToolCallStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorAqua},
			BackgroundAttributes: term.Attributes{},
		},
		SelectionConfig: SelectionConfig{
			TitleStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.ColorSilver},
			},
			OptionStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.ColorSilver},
			},
			CursorStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Attrs: term.AttrReverse},
			},
			HeaderStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.ColorYellow},
			},
			DescStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.GetColor("dimgray")},
			},
		},
		// Mirrors the command prompt overlay shipped in cmd/rune/rune.star
		// ("command.*_attr") rather than the library fallbacks, which the
		// shipped config overrides.
		CompletionMatchedTextAttr: term.Attributes{
			Fg: term.ColorBlue, Attrs: term.AttrBold,
		},
		CompletionFocusElementAttr: term.Attributes{Fg: term.ColorPurple},
		CompletionElementAttr:      term.Attributes{Attrs: term.AttrDim},
		// Deliberately neutral rather than the command prompt's red: the
		// shader bakes its blend into literal RGB inside this process,
		// which has no access to the host's themed palette, so a hue would
		// clash with themes that remap it.
		CompletionRadarColor: term.GetColor("dimgray"),
		InlineAttachmentAttr: term.Attributes{
			Fg: term.ColorAqua, Attrs: term.AttrUnderline,
		},
	}
}

// DefaultMarkdownConfig returns the markdown config the transcript uses
// when ComponentConfig.MarkdownConfig is nil.
func DefaultMarkdownConfig() *markdown.Config {
	cfg := markdown.DefaultConfig()
	cfg.HeaderPrefix = false
	cfg.CodeBlockPadding = markdown.Padding{Top: 1, Right: 2, Bottom: 1, Left: 2}
	return &cfg
}

// InputBoxConfig holds styling configuration for the compose input
// frame and the prompt feedback inputbox.
type InputBoxConfig struct {
	Placeholder       string
	PlaceholderConfig component.StringResponsiveConfig
	FrameAttr         term.Attributes
	FrameCharSet      component.FrameCharSet
	ContentAttr       term.Attributes
	// GraphicsStorage caches the images that FrameCharSet.Graphics
	// names, which the compose box draws over its frame once a Handler
	// wraps the Component. Without it the frame shows its runes alone.
	GraphicsStorage storageapi.Service
}

// ReadConfig returns the chat that own, the rune-agent extension's config,
// and host, the config Rune serves, configure: own's colours, transcript
// layout, compose box and status bar, and the editor host's editor.mode
// names, which copies to clip. Settings own leaves out or gets wrong keep
// those of DefaultComponentConfig; noti tells the user of the status bar
// settings that are wrong, which would otherwise go unnoticed.
func ReadConfig(
	own, host config.Config, clip clipboard.Register, noti browserapi.Notifications,
) ComponentConfig {
	ret := DefaultComponentConfig()
	backgroundAttr := readBackgroundAttr(own)
	ret.ReceiveMessageStringConfig.BackgroundAttributes = backgroundAttr
	ret.InputBox.PlaceholderConfig.BackgroundAttributes = backgroundAttr

	ret.InputBox.PlaceholderConfig.Bg = backgroundAttr.Bg

	ret.ReceiveMessageStringConfig.Bg = backgroundAttr.Bg

	sendMsgBackgroundAttr, err := config.GetAttributes(own, "user_msg_background_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'user_msg_background_attr' from extension config", "error", err)
		}
		sendMsgBackgroundAttr = term.Attributes{Bg: term.ColorGray}
	}
	ret.SendMessageStringConfig.BackgroundAttributes = sendMsgBackgroundAttr
	ret.SendMessageStringConfig.Bg = sendMsgBackgroundAttr.Bg
	ret.ReasoningStringConfig.BackgroundAttributes = backgroundAttr
	ret.ReasoningStringConfig.Bg = backgroundAttr.Bg
	ret.ToolCallStringConfig.BackgroundAttributes = backgroundAttr
	ret.ToolCallStringConfig.Bg = backgroundAttr.Bg
	ret.ToolCallArgsStringConfig.BackgroundAttributes = backgroundAttr
	ret.ToolCallArgsStringConfig.Bg = backgroundAttr.Bg
	ret.ToolResultStringConfig.BackgroundAttributes = backgroundAttr
	ret.ToolResultStringConfig.Bg = backgroundAttr.Bg
	ret.ReasoningAnnotationStringConfig.BackgroundAttributes = backgroundAttr
	ret.ReasoningAnnotationStringConfig.Bg = backgroundAttr.Bg
	ret.ErrorStringConfig.BackgroundAttributes = backgroundAttr
	ret.ErrorStringConfig.Bg = backgroundAttr.Bg
	ret.WarningStringConfig.BackgroundAttributes = backgroundAttr
	ret.WarningStringConfig.Bg = backgroundAttr.Bg
	ret.PromptToolCallStringConfig.BackgroundAttributes = backgroundAttr
	ret.PromptToolCallStringConfig.Bg = backgroundAttr.Bg
	ret.SelectionConfig.TitleStringConfig.BackgroundAttributes = backgroundAttr
	ret.SelectionConfig.TitleStringConfig.Bg = backgroundAttr.Bg
	ret.SelectionConfig.OptionStringConfig.BackgroundAttributes = backgroundAttr
	ret.SelectionConfig.OptionStringConfig.Bg = backgroundAttr.Bg
	ret.SelectionConfig.CursorStringConfig.BackgroundAttributes = backgroundAttr
	ret.SelectionConfig.CursorStringConfig.Bg = backgroundAttr.Bg
	ret.SelectionConfig.HeaderStringConfig.BackgroundAttributes = backgroundAttr
	ret.SelectionConfig.HeaderStringConfig.Bg = backgroundAttr.Bg
	ret.SelectionConfig.DescStringConfig.BackgroundAttributes = backgroundAttr
	ret.SelectionConfig.DescStringConfig.Bg = backgroundAttr.Bg
	ret.BackgroundColor = backgroundAttr.Bg

	sendMsgAttr, err := config.GetAttributes(own, "user_msg_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'user_msg_attr' from extension config", "error", err)
		}
	} else {
		ret.SendMessageStringConfig.Attributes = sendMsgAttr
	}
	ret.PinnedMessageMaxRows = resolvePinnedUserMsgMaxLines(own)
	ret.MessagesRowConfig.PadHorizontal = resolveTranscriptHorizontalPadding(own)
	ret.SendMessageIcon = resolveUserMsgIcon(ret.SendMessageIcon, own)
	recvMsgAttr, err := config.GetAttributes(own, "assistant_msg_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'assistant_msg_attr' from extension config", "error", err)
		}
	} else {
		ret.ReceiveMessageStringConfig.Attributes = recvMsgAttr
	}

	ret.InputBox.FrameAttr, ret.InputBox.ContentAttr, ret.InputBackgroundColor = inputBoxColors(own, backgroundAttr.Bg)
	inputBoxPlaceholderAttr, err := config.GetAttributes(own, "input_box_placeholder_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'input_box_placeholder_attr' from extension config", "error", err)
		}
	} else {
		ret.InputBox.PlaceholderConfig.Attributes = inputBoxPlaceholderAttr
	}

	ret.InputBox.FrameCharSet = inputBoxFrameCharSet(own)

	if attr, err := config.GetAttributes(own, "completion_matched_text_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_matched_text_attr' from extension config", "error", err)
		}
	} else {
		ret.CompletionMatchedTextAttr = attr
	}
	if attr, err := config.GetAttributes(own, "completion_focus_element_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_focus_element_attr' from extension config", "error", err)
		}
	} else {
		ret.CompletionFocusElementAttr = attr
	}
	if attr, err := config.GetAttributes(own, "completion_element_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_element_attr' from extension config", "error", err)
		}
	} else {
		ret.CompletionElementAttr = attr
	}
	if col, err := own.GetColor("completion_radar_color"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_radar_color' from extension config", "error", err)
		}
	} else {
		ret.CompletionRadarColor = col
	}
	if attr, err := config.GetAttributes(own, "inline_attachment_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'inline_attachment_attr' from extension config", "error", err)
		}
	} else {
		ret.InlineAttachmentAttr = attr
	}
	ret.StatusBar = statusBarConfig(own, host, noti)
	ret.StatusBar.DurationPrecision = ret.DurationPrecision

	ret.Clipboard = clip
	// On error the compose editor stays nil and the chat falls back to
	// its inputbox.
	if editor, err := extutil.Editor(clip, host); err != nil {
		slog.Warn("resolve dialogue editor, using inputbox", "error", err)
	} else {
		ret.Editor = editor
		if modal, err := extutil.EditorModal(host); err != nil {
			slog.Warn("resolve dialogue editor mode", "error", err)
		} else {
			ret.EditorModal = modal
		}
	}
	ret.ModalStartInsert = true
	if startInsert, err := own.GetBool("editor_modal_start_insert"); err == nil {
		ret.ModalStartInsert = startInsert
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'editor_modal_start_insert' from config", "error", err)
	}
	return ret
}

// ReadStyle returns cfg with the status bar, the compose box frame and
// the compose box colours ReadConfig reads from own and host, and the
// images of that frame. They are what of a chat's look
// Component.SetStatusBarConfig, Component.SetInputBoxFrameCharSet and
// Component.SetInputBoxColors change while it is open. Their images are
// read again rather than taken from the copies cfg's storage caches, so
// an image overwritten in place shows; an image that does not load is
// left out.
func ReadStyle(
	ctx context.Context, cfg ComponentConfig, own, host config.Config, noti browserapi.Notifications,
) (ComponentConfig, map[string]stdimage.Image) {
	bar := statusBarConfig(own, host, noti)
	bar.DurationPrecision = cfg.DurationPrecision
	bar.Storage = cfg.StatusBar.Storage
	bar.Images = refreshStatusBarImages(ctx, bar.Layout, bar.Storage)
	cfg.StatusBar = bar
	cfg.InputBox.FrameCharSet = inputBoxFrameCharSet(own)
	cfg.InputBox.FrameAttr, cfg.InputBox.ContentAttr, cfg.InputBackgroundColor = inputBoxColors(
		own, readBackgroundAttr(own).Bg)
	var frameImages map[string]stdimage.Image
	if cfg.InputBox.GraphicsStorage != nil {
		frameImages = refreshFrameImages(ctx, cfg.InputBox.FrameCharSet.Graphics, cfg.InputBox.GraphicsStorage)
	}
	return cfg, frameImages
}

// readBackgroundAttr reads background_attr from own, which is zero when
// own leaves it out or gets it wrong.
func readBackgroundAttr(own config.Config) term.Attributes {
	attr, err := config.GetAttributes(own, "background_attr")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'background_attr' from extension config", "error", err)
	}
	return attr
}

// inputBoxColors reads from own the attributes of the compose box frame
// and of the prompt feedback inputbox, whose background is bg unless own
// sets them, and the background of the compose input, which is
// term.ColorDefault unless own sets it.
func inputBoxColors(own config.Config, bg term.Color) (frame, content term.Attributes, input term.Color) {
	def := DefaultComponentConfig()
	frame, content = def.InputBox.FrameAttr, def.InputBox.ContentAttr
	frame.Bg, content.Bg = bg, bg
	if attr, err := config.GetAttributes(own, "input_box_attr"); err == nil {
		content = attr
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'input_box_attr' from extension config", "error", err)
	}
	if attr, err := config.GetAttributes(own, "input_box_frame_attr"); err == nil {
		frame = attr
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'input_box_frame_attr' from extension config", "error", err)
	}
	input = resolveInputBackgroundColor(def.InputBackgroundColor, own)
	if col, err := own.GetColor("input_background_color"); err == nil {
		input = col
	} else if !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'input_background_color' from extension config", "error", err)
	}
	return frame, content, input
}

// inputBoxFrameCharSet reads input_box_frame_charset from own, whose
// missing slots keep those of the default frame. An invalid charset is
// the default one.
func inputBoxFrameCharSet(own config.Config) component.FrameCharSet {
	def := DefaultComponentConfig().InputBox.FrameCharSet
	if def == (component.FrameCharSet{}) {
		def = component.FrameCharSetDefault()
	}
	cs, err := tconfig.GetFrameCharset(own, "input_box_frame_charset", def)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'input_box_frame_charset' from extension config", "error", err)
	}
	return cs
}

// refreshFrameImages reads the images g names again, dropping the copies
// storage caches, and returns them by URI. An image that does not load
// is left out.
func refreshFrameImages(
	ctx context.Context, g component.FrameCharSetGraphics, storage storageapi.Service,
) map[string]stdimage.Image {
	images := make(map[string]stdimage.Image)
	tried := make(map[string]struct{})
	for _, uri := range []string{
		g.TopLeft, g.TopRight, g.BottomLeft, g.BottomRight,
		g.HorizontalTop, g.HorizontalBottom, g.VerticalLeft, g.VerticalRight,
	} {
		if _, ok := tried[uri]; ok || uri == "" {
			continue
		}
		tried[uri] = struct{}{}
		img, err := imageuri.Refresh(ctx, uri, template.DefaultImageTTL, storage)
		if err != nil {
			slog.Warn("reload frame image", "error", err)
			continue
		}
		images[uri] = img
	}
	return images
}

// resolveInputBackgroundColor returns the compose input background color.
// It keeps def unless input_box_attr.bg is set to an explicit,
// non-default color, letting the shared input_box_attr drive the compose
// background without a dedicated key.
func resolveInputBackgroundColor(def term.Color, own config.Config) term.Color {
	attr, err := config.GetAttributes(own, "input_box_attr")
	if err != nil || attr.Bg == term.ColorDefault {
		return def
	}
	return attr.Bg
}

// resolvePinnedUserMsgMaxLines reads pinned_user_msg_max_lines, the rows
// the pinned user message may take over the top of the transcript. Zero
// disables the pin, and a negative value is clamped to it.
func resolvePinnedUserMsgMaxLines(own config.Config) int {
	v, err := own.GetInt("pinned_user_msg_max_lines")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'pinned_user_msg_max_lines' from config", "error", err)
		}
		return DefaultPinnedMessageMaxRows
	}
	return max(0, v)
}

// resolveTranscriptHorizontalPadding reads transcript_horizontal_padding,
// the columns left beside the transcript, split evenly on either side. A
// negative value is the transcript's width instead, and the transcript
// fills a tab narrower than that. Zero fills the tab.
func resolveTranscriptHorizontalPadding(own config.Config) int {
	v, err := own.GetInt("transcript_horizontal_padding")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'transcript_horizontal_padding' from config", "error", err)
		}
		return DefaultComponentConfig().MessagesRowConfig.PadHorizontal
	}
	return v
}

// resolveUserMsgIcon reads user_msg_icon, the character marking each of
// the user's messages in the chat. An empty string hides the icon. Any
// other value must be a single character, as the icon takes exactly one
// cell; a longer value keeps def rather than being cut short.
func resolveUserMsgIcon(def rune, own config.Config) rune {
	v, err := own.GetString("user_msg_icon")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'user_msg_icon' from config", "error", err)
		}
		return def
	}
	icon := []rune(v)
	switch len(icon) {
	case 0:
		return 0
	case 1:
		return icon[0]
	}
	slog.Warn("'user_msg_icon' must be a single character", "value", v)
	return def
}
