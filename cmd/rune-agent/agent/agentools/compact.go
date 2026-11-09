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

package agentools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
)

type compactTool struct{}

type compactArgs struct {
	Instructions *string `json:"instructions"`
}

func newCompact() agent.Tool { return &compactTool{} }

func (t *compactTool) NeedsDeterministicOrder() bool { return false }

func (t *compactTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "compact",
			Description: "Compact the conversation by summarizing it into a fresh dialogue. " +
				"Use this when the conversation has grown large and older tool results " +
				"are no longer needed verbatim. The current conversation is preserved " +
				"immutably; a new one is created from the summary.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instructions": map[string]any{
						"type": []string{"string", "null"},
						"description": "Optional guidance for the summarizer: what must survive " +
							"compaction verbatim (e.g. the current plan, failing output, " +
							"decisions) and what can be dropped.",
					},
				},
				"required":             []string{"instructions"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *compactTool) Execute(_ context.Context, arguments string) agent.ToolResult {
	var args compactArgs
	// Calls recorded before the tool took arguments may carry none.
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
		}
	}
	var instructions string
	if args.Instructions != nil {
		instructions = strings.TrimSpace(*args.Instructions)
	}
	return agent.ToolResult{
		Content:             "Compacting conversation...",
		Compact:             true,
		CompactInstructions: instructions,
	}
}

func (t *compactTool) Summary(_ string) string { return "" }
