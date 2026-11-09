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

import "context"

// PromptOption describes a single selectable choice.
type PromptOption struct {
	Label       string
	Description string
	Value       string
	// RequiresInput indicates that selecting this option should
	// open a text input box so the user can type a response.
	RequiresInput bool
}

// PromptKind says what a prompt waits for.
type PromptKind uint8

const (
	// PromptKindQuestion waits for the user to answer a question.
	PromptKindQuestion PromptKind = iota
	// PromptKindPermission waits for the user to approve something.
	PromptKindPermission
)

// PromptRequest describes a prompt to show to the user.
type PromptRequest struct {
	Title       string
	Header      string // short chip label (max 12 chars)
	Body        string // optional markdown content shown before options
	Options     []PromptOption
	MultiSelect bool
	// Kind says what the prompt waits for, so a host can tell an
	// approval from a question where the chat is not in sight.
	Kind PromptKind
}

// PromptResponse holds the user's selection(s).
type PromptResponse struct {
	Values    []string // selected option Value(s)
	TextInput string   // text typed by the user (when a RequiresInput option was selected)
}

// Prompter allows tools to block and wait for user input.
type Prompter interface {
	Prompt(ctx context.Context, req PromptRequest) (PromptResponse, error)
}
