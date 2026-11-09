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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

// ExitPlanTool implements the exit_plan_mode tool that persists the
// plan to the given directory and prompts the user for approval.
type ExitPlanTool struct {
	plansDir string
	prompter agent.Prompter

	// GeneratePlanPath, when non-nil, replaces the default path generation
	// (directory + timestamped filename). It receives the plan title and must
	// return the full file path. Intended for testing.
	GeneratePlanPath func(title string) string
}

type exitPlanArgs struct {
	Title string `json:"title"`
	Plan  string `json:"plan"`
}

const (
	approveValue  = "approve"
	feedbackValue = "feedback"
)

// NewExitPlan creates an exit_plan_mode tool that persists the
// plan to the given directory and prompts the user for approval.
func NewExitPlan(plansDir string, prompter agent.Prompter) *ExitPlanTool {
	return &ExitPlanTool{plansDir: plansDir, prompter: prompter}
}

// Definition returns the LLM tool definition for exit_plan_mode.
func (t *ExitPlanTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "exit_plan_mode",
			Description: `Use this tool when you have finished writing your plan and are ready
for user approval. This tool saves the plan to disk and presents it
to the user for review.

How this tool works:
- Pass the plan title and full markdown content.
- The tool saves the plan to disk and asks the user to approve or
  give feedback.
- If the user approves, the tool returns success. Output the plan
  as your final response so the parent agent can execute it.
- If the user gives feedback, the tool returns their feedback.
  You MUST refine the plan based on the feedback and call this tool
  again. Do NOT respond with text — always call exit_plan_mode.

When to use:
- ONLY when the task requires planning implementation steps for
  writing code. Do NOT use for pure research or exploration tasks.

Before using:
- Ensure your plan is complete and unambiguous.
- If you have unresolved questions about requirements or approach,
  use ask_user_question first (in earlier phases).
- Once your plan is finalized, use THIS tool to request approval.

Important: Do NOT use ask_user_question to ask "Is this plan okay?"
or "Should I proceed?" — that is exactly what THIS tool does.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "Short title for the plan (used as filename).",
					},
					"plan": map[string]any{
						"type":        "string",
						"description": "The full plan content in markdown.",
					},
				},
				"required":             []string{"title", "plan"},
				"additionalProperties": false,
			},
		},
	}
}

// Summary returns a short human-readable summary of the tool arguments.
func (t *ExitPlanTool) Summary(arguments string) string {
	var args exitPlanArgs
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	title := args.Title
	if len(title) > 60 {
		title = title[:60] + "..."
	}
	return title
}

// NeedsDeterministicOrder reports that exit_plan writes the plan file
// to disk and so must run sequentially within a batch.
func (t *ExitPlanTool) NeedsDeterministicOrder() bool { return true }

// Execute persists the plan and asks the user to approve or revise it.
func (t *ExitPlanTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	var args exitPlanArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("invalid arguments: %v", err),
			IsError: true,
		}
	}
	if args.Title == "" || args.Plan == "" {
		return agent.ToolResult{
			Content: "invalid arguments: title and plan are required",
			IsError: true,
		}
	}

	return t.execute(ctx, args.Title, args.Plan)
}

// execute persists the plan and prompts the user for approval.
func (t *ExitPlanTool) execute(ctx context.Context, title, plan string) agent.ToolResult {
	path, err := t.writePlan(title, plan)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("save plan: %v", err),
			IsError: true,
		}
	}

	resp, err := t.prompter.Prompt(ctx, agent.PromptRequest{
		Title:  "Plan ready for review",
		Header: "Plan",
		Body:   plan,
		Kind:   agent.PromptKindPermission,
		Options: []agent.PromptOption{
			{
				Label:       "Approve",
				Description: "Accept the plan and start implementing",
				Value:       approveValue,
			},
			{
				Label:         "Give feedback",
				Description:   "Suggest changes to the plan",
				Value:         feedbackValue,
				RequiresInput: true,
			},
		},
	})
	if err != nil {
		return agent.ToolResult{
			Content: "The user dismissed the prompt without making a selection. This is not an error — the user may have dismissed it accidentally. Please call exit_plan_mode again with the same plan to re-prompt the user.",
		}
	}

	if len(resp.Values) > 0 && resp.Values[0] == approveValue {
		return agent.ToolResult{
			Content: fmt.Sprintf("Plan approved. Saved to %s\n\n%s", path, plan),
			ApprovedPlan: &dialoguemanager.ApprovedPlan{
				Path: path,
				Body: plan,
			},
			ClearContext: true,
		}
	}

	feedback := resp.TextInput
	if feedback == "" {
		feedback = "User wants to give feedback on the plan."
	}
	feedback = fmt.Sprintf("User feedback: %s\n\nPlease revise the plan and call exit_plan_mode again.", feedback)
	return agent.ToolResult{
		Content: fmt.Sprintf("Plan saved to %s. %s", path, feedback),
	}
}

func (t *ExitPlanTool) writePlan(title, plan string) (string, error) {
	var path string
	if t.GeneratePlanPath != nil {
		path = t.GeneratePlanPath(title)
	} else {
		slug := slugify(title)
		ts := time.Now().Format("20060102-150405")
		path = filepath.Join(t.plansDir, ts+"-"+slug+".md")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create plans directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
		return "", fmt.Errorf("write plan file: %w", err)
	}
	return path, nil
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "plan"
	}
	return s
}
