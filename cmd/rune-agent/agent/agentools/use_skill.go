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
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
)

// NewSkillTool creates a skill tool backed by a skill registry.
// The tool always sees the latest set of skills, even after runtime
// add-dir/remove-dir operations. When spawner is non-nil, agent-type
// skills are executed by spawning a sub-agent via the spawner.
// childEvents receives sub-agent events tagged with the parent tool
// call ID for TUI rendering.
func NewSkillTool(
	registry *skills.SkillRegistry, spawner agent.Spawner,
	childEvents chan<- agent.ChildEvent,
) agent.Tool {
	return &skillTool{registry: registry, spawner: spawner, childEvents: childEvents}
}

type skillTool struct {
	registry    *skills.SkillRegistry
	spawner     agent.Spawner
	childEvents chan<- agent.ChildEvent
	// policy hides the skills the agent may not use.
	policy toolpolicy.Policy
}

// WithPolicy implements agent.PolicyTool: the copy hides the skills p
// does not allow, as if they did not exist.
func (t *skillTool) WithPolicy(p toolpolicy.Policy) agent.Tool {
	c := *t
	c.policy = p
	return &c
}

type skillArgs struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

func (t *skillTool) NeedsDeterministicOrder() bool { return false }

func (t *skillTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name: "skill",
			Description: `Load and execute a skill by name. When the user's request matches a
skill listed in the system-reminder, call this tool BEFORE generating
any other response. Do not mention a skill without calling this tool.
If a <skill_content> block is already present in the current turn,
the skill has been loaded — follow its instructions directly without
calling this tool again.
When users reference a slash command or /<something> (e.g. "/review",
"/commit"), they are referring to a skill. Use this tool to invoke it.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "The skill name to load",
					},
					"args": map[string]any{
						"type":        []string{"string", "null"},
						"description": "Optional arguments for the skill",
					},
				},
				"required":             []string{"name", "args"},
				"additionalProperties": false,
			},
		},
	}
}

func (t *skillTool) Summary(arguments string) string {
	var args skillArgs
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	if args.Args != "" {
		return args.Name + " " + args.Args
	}
	return args.Name
}

func (t *skillTool) Execute(
	ctx context.Context, arguments string,
) agent.ToolResult {
	var args skillArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("invalid arguments: %v", err),
			IsError: true,
		}
	}

	skill, ok := t.registry.Get(args.Name)
	if !ok || !t.policy.AllowsSkill(skill.Name) {
		all := t.registry.List()
		names := make([]string, 0, len(all))
		for _, s := range all {
			if t.policy.AllowsSkill(s.Name) {
				names = append(names, s.Name)
			}
		}
		return agent.ToolResult{
			Content: fmt.Sprintf(
				"unknown skill %q. Available skills: %s",
				args.Name, strings.Join(names, ", "),
			),
			IsError: true,
		}
	}

	// Agent-type skills: spawn a sub-agent instead of injecting inline.
	if skill.Type == "agent" {
		return t.executeAgentSkill(ctx, skill, args.Args)
	}

	// Always return the full <skill_content> body, even on repeat
	// invocations. A "see the block above" hint was tried and proved
	// unreliable: the model frequently fails to locate a transient
	// system message (especially for slash-command preloads, where
	// the user message is just the <command-name> envelope) and
	// gives up instead of following the skill. Re-injecting the body
	// costs a few tokens but guarantees the instructions are present
	// in the tool result the model is already attending to.
	return agent.ToolResult{Content: skills.FormatSkillContent(skill)}
}

// executeAgentSkill spawns a sub-agent with the skill's body as system
// prompt and its tool policy. It blocks until the sub-agent completes and
// returns its output.
func (t *skillTool) executeAgentSkill(
	ctx context.Context, skill skills.Skill, userArgs string,
) agent.ToolResult {
	if t.spawner == nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("skill %q is an agent skill but no spawner is available", skill.Name),
			IsError: true,
		}
	}

	task := userArgs
	if task == "" {
		task = skill.Body
	}

	handle, err := t.spawner.Run(ctx, agent.RunRequest{
		Label:        skill.Name,
		Model:        llmarg.Qualify(agent.CurrentModel(ctx)),
		Message:      task,
		Tools:        t.registry.AgentPolicy(skill),
		SystemPrompt: skill.Body,
	})
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("agent skill %q error: %v", skill.Name, err),
			IsError: true,
		}
	}

	return consumeSubAgent(ctx, handle.Events, t.childEvents)
}
