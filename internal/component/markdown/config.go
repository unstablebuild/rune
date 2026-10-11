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

package markdown

import (
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// Config defines the styling options for markdown rendering.
type Config struct {
	// Header styles (H1-H6)
	H1, H2, H3, H4, H5, H6 term.Attributes
	// HeaderPrefix controls whether headers display the '#' prefix (e.g., "# Title").
	// When true (default), headers show the markdown prefix to indicate nesting level.
	HeaderPrefix bool

	// Text styles
	Paragraph     term.Attributes
	Bold          term.Attributes
	Italic        term.Attributes
	Strikethrough term.Attributes

	// Code styles
	CodeBlock  term.Attributes
	InlineCode term.Attributes
	// CodeBlockPadding insets code within the code block background. The
	// horizontal padding is dropped when it would leave no column for code.
	CodeBlockPadding Padding
	// CodeBlockCopy, when true, draws CodeBlockCopyIcon in the top-right
	// corner of each code block's padding to mark it as copyable. It needs
	// CodeBlockPadding.Top >= 1 and Right >= the icon's width, and is not
	// drawn otherwise. The component only draws it: hosts that enable it
	// must resolve clicks against Component.CodeBlockCopyTargets and do
	// the copying.
	CodeBlockCopy     bool
	CodeBlockCopyIcon rune
	// CodeBlockCopiedIcon replaces CodeBlockCopyIcon once the host marks
	// the block copied with Component.MarkCodeBlockCopied, until the
	// pointer leaves the icon.
	CodeBlockCopiedIcon rune
	// CodeBlockCopyIconAttr styles CodeBlockCopyIcon,
	// CodeBlockCopyIconHoverAttr styles it while hovered and
	// CodeBlockCopiedIconAttr styles CodeBlockCopiedIcon. All take the
	// CodeBlock background when it sets one.
	CodeBlockCopyIconAttr      term.Attributes
	CodeBlockCopyIconHoverAttr term.Attributes
	CodeBlockCopiedIconAttr    term.Attributes
	// Parser, when set, enables syntax highlighting inside fenced code blocks.
	Parser syntaxapi.Parser
	// ScheduleNextTick schedules a function to run on the next event-loop
	// tick. Highlighting runs asynchronously via this callback so that
	// Highlight I/O does not block construction. Defaults to a
	// synchronous call in DefaultConfig.
	ScheduleNextTick func(func()) bool

	// Link styles
	Link    term.Attributes
	LinkURL term.Attributes

	// Blockquote styling
	Blockquote       term.Attributes
	BlockquoteBorder rune

	// List styling
	ListBullet        rune
	TaskListChecked   rune
	TaskListUnchecked rune

	// Table styling
	TableCharSet TableCharSet

	// Horizontal rule styling
	HorizontalRule     rune
	HorizontalRuleAttr term.Attributes

	// Spacing
	ParagraphSpacing int

	// Search highlight styles
	SearchMatch   term.Attributes // non-current search matches
	SearchCurrent term.Attributes // current search match
}

// Padding is the number of blank cells on each side of a block.
type Padding struct {
	Top, Right, Bottom, Left int
}

// TableCharSet defines the characters used to draw table borders.
type TableCharSet struct {
	component.FrameUnionCharSet
	HeaderSeparator rune
	ColumnSeparator rune
	CrossJoin       rune
	TopJoin         rune
	BottomJoin      rune
}

// DefaultTableCharSet returns the default table character set.
func DefaultTableCharSet() TableCharSet {
	return TableCharSet{
		FrameUnionCharSet: component.DefaultFrameUnionCharSet(),
		HeaderSeparator:   '─',
		ColumnSeparator:   '│',
		CrossJoin:         '┼',
		TopJoin:           '┬',
		BottomJoin:        '┴',
	}
}

// DefaultConfig returns a Config with sensible defaults for terminal rendering.
func DefaultConfig() Config {
	magenta := term.Attributes{Fg: term.ColorFuchsia}
	magentaBold := term.Attributes{Fg: term.ColorFuchsia, Attrs: term.AttrBold}
	cyan := term.Attributes{Fg: term.ColorTeal}
	cyanUnderline := term.Attributes{Fg: term.ColorTeal, Attrs: term.AttrUnderline}
	codeblock := term.Attributes{Bg: term.ColorGray}
	gray := term.Attributes{Fg: term.ColorGray}
	def := term.Attributes{Fg: term.ColorDefault}
	defBold := term.Attributes{Fg: term.ColorDefault, Attrs: term.AttrBold}
	defItalic := term.Attributes{Fg: term.ColorDefault, Attrs: term.AttrItalic}
	dimWhite := term.Attributes{Fg: term.GetColor("dimgray")}
	dimWhiteStrike := term.Attributes{
		Fg: term.ColorDefault, Attrs: term.AttrStrikeThrough | term.AttrDim,
	}
	purpleBold := term.Attributes{Fg: term.ColorPurple, Attrs: term.AttrBold}
	purple := term.Attributes{Fg: term.ColorPurple}
	purpleDim := term.Attributes{Fg: term.ColorPurple, Attrs: term.AttrDim}

	title := term.Attributes{
		Fg:    term.ColorWhite,
		Bg:    term.ColorPurple,
		Attrs: term.AttrBold,
	}
	return Config{
		H1:           title,
		H2:           magentaBold,
		H3:           magenta,
		H4:           purpleBold,
		H5:           purple,
		H6:           purpleDim,
		HeaderPrefix: true,

		Paragraph:     def,
		Bold:          defBold,
		Italic:        defItalic,
		Strikethrough: dimWhiteStrike,

		CodeBlock:  codeblock,
		InlineCode: codeblock,

		CodeBlockCopyIcon:          '', // nf-fa-copy
		CodeBlockCopyIconAttr:      def,
		CodeBlockCopyIconHoverAttr: term.Attributes{Fg: term.ColorBlue},
		CodeBlockCopiedIcon:        '', // nf-fa-check
		CodeBlockCopiedIconAttr:    term.Attributes{Fg: term.ColorGreen},

		Link:    cyanUnderline,
		LinkURL: cyan,

		Blockquote:       dimWhite,
		BlockquoteBorder: '│',

		ListBullet:        '•',
		TaskListChecked:   '☑',
		TaskListUnchecked: '☐',

		TableCharSet: DefaultTableCharSet(),

		HorizontalRule:     '─',
		HorizontalRuleAttr: gray,

		ScheduleNextTick: func(cb func()) bool { cb(); return true },

		ParagraphSpacing: 1,

		SearchMatch:   term.Attributes{Attrs: term.AttrReverse},
		SearchCurrent: term.Attributes{Bg: term.ColorYellow, Fg: term.ColorBlack},
	}
}
