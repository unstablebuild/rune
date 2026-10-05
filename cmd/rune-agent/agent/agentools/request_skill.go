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

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
)

type requestSkillTool struct {
	prompter agent.Prompter
}

type requestSkillArgs struct {
	Skill       string `json:"skill"`
	Description string `json:"description"`
}

// NewRequestSkill creates a request_skill tool backed by the given Prompter.
// The model calls this when it needs a capability that is not currently
// available as a tool or skill. The user is prompted to install the
// skill out of band and confirm, or decline.
func NewRequestSkill(prompter agent.Prompter) agent.Tool {
	return &requestSkillTool{prompter: prompter}
}

func (t *requestSkillTool) NeedsDeterministicOrder() bool { return false }

func (t *requestSkillTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "request_skill",
			Description: `Request a skill or capability that is not currently available.
Use this when you need a tool that is not in your tool list (e.g. a
language-specific linter, a deployment tool). The user is shown what
you need and can install the skill out of band, then confirm. If the
user confirms, newly installed skills will be available on the next
turn.

Do NOT use this for skills that are already available — use the skill
tool instead.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"skill": map[string]any{
						"type":        "string",
						"description": "Short name for the skill being requested (e.g. \"golangci-lint\", \"deploy\").",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "What the skill should do and why it is needed for the current task.",
					},
				},
				"required":             []string{"skill", "description"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *requestSkillTool) Summary(arguments string) string {
	var args requestSkillArgs
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	return args.Skill
}

func (t *requestSkillTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args requestSkillArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("invalid arguments: %v", err),
			IsError: true,
		}
	}
	if args.Skill == "" {
		return agent.ToolResult{
			Content: "invalid arguments: skill name is required",
			IsError: true,
		}
	}

	resp, err := t.prompter.Prompt(ctx, agent.PromptRequest{
		Title:  fmt.Sprintf("Skill %q is not available", args.Skill),
		Header: "Skill",
		Body:   args.Description,
		Options: []agent.PromptOption{
			{Label: "Done", Description: "I have installed the skill", Value: "done"},
			{Label: "Won't do", Description: "Skip this — continue without it", Value: "wont_do"},
		},
	})
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("prompt dismissed: %v", err),
			IsError: true,
		}
	}

	if len(resp.Values) > 0 && resp.Values[0] == "done" {
		return agent.ToolResult{
			Content: fmt.Sprintf(
				"The user has installed the %q skill. "+
					"It should now be available — check the skills list and proceed.",
				args.Skill,
			),
		}
	}

	return agent.ToolResult{
		Content: fmt.Sprintf(
			"The user declined to install the %q skill. "+
				"Continue without it or find an alternative approach.",
			args.Skill,
		),
	}
}
