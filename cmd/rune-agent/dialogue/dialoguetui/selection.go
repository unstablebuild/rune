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
	"strings"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	tcomponent "unstable.build/rune/internal/component"
)

var _ component.Responsive = (*Selection)(nil)

// SelectionOption describes a single option in a Selection component.
type SelectionOption struct {
	Label       string
	Description string
	// RequiresInput indicates that selecting this option should
	// transition to a text input mode.
	RequiresInput bool
}

// SelectionConfig holds styling for the Selection component.
type SelectionConfig struct {
	TitleStringConfig  component.StringConfig
	OptionStringConfig component.StringConfig
	CursorStringConfig component.StringConfig
	HeaderStringConfig component.StringConfig
	DescStringConfig   component.StringConfig
}

// renderedOption holds pre-computed draw data for a single option.
type renderedOption struct {
	labelDefault string   // "  Label" or "[ ] Label"
	labelCursor  string   // "> Label" or ">[x] Label"
	descLines    []string // wrapped description lines (with indent)
}

// Selection is a Responsive component that renders an inline
// selectable list in the messages area.
type Selection struct {
	title       string
	header      string
	options     []SelectionOption
	cursor      int
	checked     map[int]bool
	multiSelect bool
	cfg         SelectionConfig
	width       int
	height      int

	// cached render data, rebuilt in Resize and on cursor/toggle changes
	chip       string           // "[Header]" or ""
	chipX      int              // x position for chip, -1 if not shown
	titleLines []string         // wrapped title lines
	rendered   []renderedOption // one per option
}

// NewSelection creates a new Selection component.
func NewSelection(
	title, header string,
	options []SelectionOption,
	multiSelect bool,
	cfg SelectionConfig,
) *Selection {
	s := &Selection{
		title:       title,
		header:      header,
		options:     options,
		multiSelect: multiSelect,
		cfg:         cfg,
		checked:     make(map[int]bool),
	}
	if header != "" {
		s.chip = "[" + header + "]"
	}
	return s
}

// Height satisfies component.Responsive.
func (s *Selection) Height(width int) int {
	if width <= 0 || len(s.options) == 0 {
		return 0
	}
	h := max(wrappedLines(s.title, width, 0), 1) // title lines (wrapped)
	h++                                          // blank line after title
	for _, opt := range s.options {
		h++ // label line
		if opt.Description != "" {
			h += wrappedLines(opt.Description, width, descIndent)
		}
	}
	return h
}

// Resize satisfies tui.Component.
func (s *Selection) Resize(width, height int) {
	s.width = width
	s.height = height
	s.rebuildRenderCache()
}

// rebuildRenderCache pre-computes all strings needed by Draw.
func (s *Selection) rebuildRenderCache() {
	// Wrapped title lines.
	s.titleLines = wrapText(s.title, s.width, 0)

	// Chip position: only show when title fits on one line.
	s.chipX = -1
	if s.chip != "" && len(s.titleLines) == 1 {
		chipLen := utf8.RuneCountInString(s.chip)
		startX := s.width - chipLen
		if startX > utf8.RuneCountInString(s.title)+1 {
			s.chipX = startX
		}
	}

	// Option labels and descriptions.
	s.rendered = make([]renderedOption, len(s.options))
	for i, opt := range s.options {
		s.rendered[i] = s.buildRenderedOption(i, opt)
	}
}

func (s *Selection) buildRenderedOption(idx int, opt SelectionOption) renderedOption {
	var ro renderedOption

	// Default label (not cursor).
	defaultPrefix := "  "
	if s.multiSelect {
		if s.checked[idx] {
			defaultPrefix = "[x] "
		} else {
			defaultPrefix = "[ ] "
		}
	}
	ro.labelDefault = defaultPrefix + opt.Label

	// Cursor label.
	if s.multiSelect {
		cursorPrefix := ">"
		if s.checked[idx] {
			cursorPrefix += "[x] "
		} else {
			cursorPrefix += "[ ] "
		}
		ro.labelCursor = cursorPrefix + opt.Label
	} else {
		ro.labelCursor = "> " + opt.Label
	}

	// Wrapped description lines.
	if opt.Description != "" {
		ro.descLines = wrapText(opt.Description, s.width, descIndent)
	}

	return ro
}

// Draw satisfies tui.Component.
func (s *Selection) Draw(w term.Writer) {
	if s.width <= 0 || s.height <= 0 || len(s.options) == 0 {
		return
	}

	y := 0

	// Title lines (wrapped).
	for i, line := range s.titleLines {
		if y >= s.height {
			return
		}
		drawText(w, 0, y, line, s.cfg.TitleStringConfig.Attributes, s.width)
		if i == 0 && s.chipX >= 0 {
			drawText(w, s.chipX, y, s.chip, s.cfg.HeaderStringConfig.Attributes,
				utf8.RuneCountInString(s.chip))
		}
		y++
	}
	if y >= s.height {
		return
	}

	// Blank line after title.
	y++
	if y >= s.height {
		return
	}

	// Options.
	for i := range s.options {
		if y >= s.height {
			return
		}

		ro := s.rendered[i]
		isCursor := i == s.cursor

		label := ro.labelDefault
		attr := s.cfg.OptionStringConfig.Attributes
		if isCursor {
			label = ro.labelCursor
			attr = s.cfg.CursorStringConfig.Attributes
		}
		drawText(w, 0, y, label, attr, s.width)
		y++

		if len(ro.descLines) > 0 && y < s.height {
			descAttr := s.cfg.DescStringConfig.Attributes
			for _, line := range ro.descLines {
				if y >= s.height {
					return
				}
				drawText(w, 0, y, line, descAttr, s.width)
				y++
			}
		}
	}
}

func drawText(
	w term.Writer, x, y int, text string, attr term.Attributes, maxWidth int,
) {
	tcomponent.WriteText(w, x, y, x+maxWidth, text, attr)
}

// MoveUp moves the cursor up, wrapping to the bottom.
func (s *Selection) MoveUp() {
	if len(s.options) == 0 {
		return
	}
	s.cursor--
	if s.cursor < 0 {
		s.cursor = len(s.options) - 1
	}
}

// MoveDown moves the cursor down, wrapping to the top.
func (s *Selection) MoveDown() {
	if len(s.options) == 0 {
		return
	}
	s.cursor++
	if s.cursor >= len(s.options) {
		s.cursor = 0
	}
}

// Toggle toggles the check state of the current item (multiSelect only).
// It rebuilds the affected option's render cache.
func (s *Selection) Toggle() {
	if !s.multiSelect || len(s.options) == 0 {
		return
	}
	s.checked[s.cursor] = !s.checked[s.cursor]
	if s.rendered != nil {
		s.rendered[s.cursor] = s.buildRenderedOption(s.cursor, s.options[s.cursor])
	}
}

// Selected returns the labels of selected options.
// Single select: returns the label at the cursor.
// Multi select: returns all checked labels.
func (s *Selection) Selected() []string {
	if len(s.options) == 0 {
		return nil
	}
	if !s.multiSelect {
		return []string{s.options[s.cursor].Label}
	}
	var result []string
	for i, opt := range s.options {
		if s.checked[i] {
			result = append(result, opt.Label)
		}
	}
	return result
}

// CursorRequiresInput returns true if the option at the cursor
// has the RequiresInput flag set.
func (s *Selection) CursorRequiresInput() bool {
	if len(s.options) == 0 {
		return false
	}
	return s.options[s.cursor].RequiresInput
}

// CursorLabel returns the label of the option under the cursor,
// regardless of check state. Unlike Selected(), which in multi-select
// mode returns only the checked labels, this always identifies the
// option the cursor is currently on — the one StartPromptInput is
// about to collect free text for.
func (s *Selection) CursorLabel() string {
	if len(s.options) == 0 {
		return ""
	}
	return s.options[s.cursor].Label
}

const descIndent = 6

// wrappedLines returns the number of lines needed to render text
// within width, with an indent prefix on each line.
func wrappedLines(text string, width, indent int) int {
	usable := width - indent
	if usable <= 0 {
		return 1
	}
	textLen := utf8.RuneCountInString(text)
	return max((textLen+usable-1)/usable, 1)
}

// wrapText wraps text into lines of at most (width - indent) runes,
// with indent spaces of padding on each line.
func wrapText(text string, width, indent int) []string {
	usable := width - indent
	if usable <= 0 {
		return []string{strings.Repeat(" ", indent) + text}
	}
	pad := strings.Repeat(" ", indent)
	runes := []rune(text)
	var lines []string
	for len(runes) > 0 {
		end := min(usable, len(runes))
		lines = append(lines, pad+string(runes[:end]))
		runes = runes[end:]
	}
	if len(lines) == 0 {
		lines = []string{pad}
	}
	return lines
}
