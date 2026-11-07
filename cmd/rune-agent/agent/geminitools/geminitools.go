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

// Package geminitools provides Gemini-specialized tools that present the
// Antigravity-native tool names and argument schemas Gemini 3 was post-trained
// to call (e.g. run_command, grep_search, codebase_search). Each tool
// translates Antigravity-shaped arguments into the schema of an underlying base
// agent.Tool and delegates execution, so the behavior is identical to the
// provider-neutral tools while the model sees the names and parameters it
// expects. Registered for the "gemini" provider via Register.
package geminitools

import (
	"context"
	"encoding/json"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
)

// LLMProvider is the provider key under which these tools are registered.
const LLMProvider = "gemini"

// translator converts an Antigravity-shaped argument JSON document into the
// argument JSON expected by the underlying base tool. A translation error is
// surfaced to the model as an error ToolResult.
type translator func(arguments string) (string, error)

// specializedTool advertises an Antigravity tool definition but delegates
// execution to a base tool after translating arguments.
type specializedTool struct {
	def       llmapi.Tool
	base      agent.Tool
	translate translator
	summarize func(arguments string) string
	// runsCommands reports that the base tool runs shell commands, whose
	// restrictions the definition then describes.
	runsCommands bool
}

func (t *specializedTool) Definition() llmapi.Tool { return t.def }

// WithPolicy implements agent.PolicyTool: the copy delegates to its base
// restricted by p, when the base depends on the policy.
func (t *specializedTool) WithPolicy(p toolpolicy.Policy) agent.Tool {
	base, ok := t.base.(agent.PolicyTool)
	if !ok {
		return t
	}
	c := *t
	c.base = base.WithPolicy(p)
	if help := p.CommandHelp(); help != "" && t.runsCommands {
		c.def.Function.Description += "\n\n" + help
	}
	return &c
}

func (t *specializedTool) NeedsDeterministicOrder() bool {
	return t.base.NeedsDeterministicOrder()
}

func (t *specializedTool) Execute(ctx context.Context, arguments string) agent.ToolResult {
	translated, err := t.translate(arguments)
	if err != nil {
		return agent.ToolResult{
			Content: "error: invalid arguments: " + err.Error(),
			IsError: true,
		}
	}
	return t.base.Execute(ctx, translated)
}

func (t *specializedTool) Summary(arguments string) string {
	if t.summarize != nil {
		return t.summarize(arguments)
	}
	if translated, err := t.translate(arguments); err == nil {
		return t.base.Summary(translated)
	}
	return ""
}

// remarshal decodes src into a fresh map and re-encodes it, used by
// translators that rename or drop fields.
func remarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// geminiSpecializations maps a base tool name to a constructor that wraps it
// with the Antigravity-native specialization. The base tool must exist in the
// registry; specializations for absent base tools are skipped.
var geminiSpecializations = map[string]func(agent.Tool) agent.Tool{
	"bash":           runCommand,
	"search_content": grepSearch,
	"find_files":     find,
	"search_symbols": codebaseSearch,
	"outline_file":   viewFileOutline,
	"list_dir":       listDirectory,
}

// Register installs the Gemini-specialized tools as overrides for the gemini
// provider, excluding the corresponding base tool names so Gemini only sees the
// Antigravity-native set. Base tools absent from r are skipped.
func Register(r *agent.Registry) {
	for baseName, wrap := range geminiSpecializations {
		base, ok := r.Get(baseName, "")
		if !ok {
			continue
		}
		r.RegisterReplacement(LLMProvider, baseName, wrap(base))
	}
}
