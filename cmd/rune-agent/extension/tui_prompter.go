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

package extension

import (
	"context"
	"errors"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// tuiPrompter bridges agent.Prompter to the dialoguetui event channel
// so prompts surface as inline selection UI in the chat tab.
type tuiPrompter struct {
	tx   chan<- dialoguetui.MessageEvent
	noti browserapi.Notifications
	// status is the chat the prompt belongs to, whose bar reports
	// phaseAsking for as long as the prompt is pending.
	status syncComponent
}

func (tp *tuiPrompter) Prompt(ctx context.Context, req agent.PromptRequest) (agent.PromptResponse, error) {
	resultCh := make(chan []string, 1)

	// Build label→value mapping so we can translate the TUI's
	// label-based selections back into the option Values that
	// callers compare against.
	labelToValue := make(map[string]string, len(req.Options))
	options := make([]dialoguetui.PromptEventOption, len(req.Options))
	for i, o := range req.Options {
		options[i] = dialoguetui.PromptEventOption{
			Label:         o.Label,
			Description:   o.Description,
			RequiresInput: o.RequiresInput,
		}
		if o.Value != "" {
			labelToValue[o.Label] = o.Value
		}
	}

	ev := dialoguetui.MessageEvent{
		Type:              dialoguetui.MessageEventPrompt,
		PromptTitle:       req.Title,
		PromptHeader:      req.Header,
		PromptBody:        req.Body,
		PromptOptions:     options,
		PromptMultiSelect: req.MultiSelect,
		PromptResult:      resultCh,
	}
	select {
	case tp.tx <- ev:
	case <-ctx.Done():
		return agent.PromptResponse{}, ctx.Err()
	}
	// Restoring the phase the prompt displaced, rather than assuming
	// the turn was executing tools, is what lets prompts that overlap
	// unwind in order: the last one to open hands back phaseAsking and
	// the first hands back the turn's own phase.
	prevPhase := tp.status.swapPhase(phaseAsking)
	defer tp.status.swapPhase(prevPhase)
	_, _ = tp.noti.Notify(browserapi.LevelWarn, "Input required: %s", req.Title)
	select {
	case vals := <-resultCh:
		if vals == nil {
			return agent.PromptResponse{}, errors.New("prompt dismissed")
		}

		// Free-form prompt (zero options): the TUI sends [text].
		// Return as TextInput only; Values stays nil.
		if len(req.Options) == 0 {
			return agent.PromptResponse{TextInput: vals[0]}, nil
		}

		// Map labels back to values where a mapping exists.
		var textInput string
		// When the TUI sends [label, text], the second element
		// is free-form text from a RequiresInput option.
		if len(vals) == 2 {
			textInput = vals[1]
			vals = vals[:1] // keep only the label for value mapping
		}
		for i, v := range vals {
			if mapped, ok := labelToValue[v]; ok {
				vals[i] = mapped
			}
		}
		return agent.PromptResponse{Values: vals, TextInput: textInput}, nil
	case <-ctx.Done():
		select {
		case tp.tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventPromptDismiss}:
		default:
		}
		return agent.PromptResponse{}, ctx.Err()
	}
}
