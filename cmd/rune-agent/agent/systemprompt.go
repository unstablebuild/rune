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

import (
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
)

// DefaultCommitAttribution is Rune Agent's model-aware commit trailer template.
const DefaultCommitAttribution = "Co-Authored-By: Rune Agent ({{provider}}/{{model}}) <agent@rune.build>"

// Attribution controls the attribution text included in agent-created commits.
// A nil or empty Commit disables attribution.
type Attribution struct {
	Commit *string
}

// DefaultAttribution returns Rune Agent's standard commit attribution.
func DefaultAttribution() Attribution {
	commit := DefaultCommitAttribution
	return Attribution{Commit: &commit}
}

// CommitAttributionInstructions returns commit instructions for the resolved model.
func CommitAttributionInstructions(model llmapi.ModelEntry, attribution Attribution) string {
	if attribution.Commit == nil || *attribution.Commit == "" {
		return ""
	}
	commit := *attribution.Commit
	commit = strings.NewReplacer(
		"{{provider}}", model.Provider,
		"{{model}}", model.Name,
	).Replace(commit)
	return fmt.Sprintf(`# Committing changes with git

Only create commits when explicitly requested by the user. When creating a commit, end the commit message with this attribution text:

%s

Use a heredoc or equivalent multiline input so the attribution is separated from the commit body by a blank line.`, commit)
}

// DefaultSystemPrompt returns the system prompt for the coding agent.
func DefaultSystemPrompt(cwd workspaceapi.URI) string {
	return fmt.Sprintf(`You are a coding assistant running inside the Rune IDE.

Workspace root: %s

Guidelines:
- Only use the tools provided. Do not attempt to call tools that are not in your tool list.
- Always read a file before applying patches, so you understand its current contents.
- When updating files via apply_patch, include enough context lines to uniquely locate each hunk.
- Explain what you are doing and why before making changes.
- Be concise in your responses.
- When running shell commands, prefer non-interactive commands.
- If a task requires multiple steps, proceed step by step, using tools as needed.

Tool strategy:
- When navigating to a definition, finding implementations, or searching
  for symbols, always use find_definition, find_implementations, or
  search_symbols. These use the language server for precise, semantic
  results — more accurate than text search. Do NOT use grep or bash to
  search for symbol definitions or references.
- When you need a file's structure (what functions, types, or methods it
  contains), always use outline_file instead of reading the entire file.
  Do NOT use cat, head, or bash to inspect file structure.
- Use search_content and bash only for tasks that dedicated tools cannot
  handle: searching for string literals, error messages, comments,
  configuration values.
- read_file is still required before modifying a file. Use it to read
  specific line ranges once you know where to look.

Context management:
- Consider compaction when context is approaching its limit and older details
  can be summarized without losing information needed for the current task.
  Rune also compacts automatically at its configured context-usage threshold.
- New messages, task changes, and completed milestones do not by themselves
  require compaction.
- Compaction replaces the conversation contents with a summary; the original
  contents are preserved under "<dialogueid>-archived".
- When using compact, keep summarizer instructions focused on the current
  task, key decisions, files modified, progress, and remaining work.`, cwd.Path())
}

// QuerySystemPrompt returns the system prompt for the quick query agent.
// It emphasizes speed and directness over thoroughness.
func QuerySystemPrompt(cwd workspaceapi.URI) string {
	return fmt.Sprintf(`You are a fast coding assistant running inside the Rune IDE.
The user invoked a quick query command and expects a fast, direct answer.

Workspace root: %s

Guidelines:
- Prioritize speed of execution. Answer quickly and concisely.
- Only use tools when strictly necessary to answer the question.
- Do not explore the codebase broadly; target exactly what is needed.
- When reading files, read only the relevant sections.
- Skip lengthy explanations. Lead with the answer or solution.
- If a task requires multiple steps, prefer the simplest approach.
- When running shell commands, prefer non-interactive commands.`, cwd.Path())
}

// skillsPromptSection returns a <system-reminder> block listing available
// skills. Returns empty string when no skills are provided.
func skillsPromptSection(loaded []skills.Skill) string {
	if len(loaded) == 0 {
		return ""
	}

	var promptSkills, agentSkills []skills.Skill
	for _, s := range loaded {
		if s.Type == "agent" {
			agentSkills = append(agentSkills, s)
		} else {
			promptSkills = append(promptSkills, s)
		}
	}

	var b strings.Builder
	b.WriteString("<system-reminder>\nThe following skills are available for use with the skill tool:\n")
	if len(promptSkills) > 0 {
		for _, s := range promptSkills {
			fmt.Fprintf(&b, "\n- %s: %s (location: %s/SKILL.md)", s.Name, s.Description, s.Dir)
		}
	}
	if len(agentSkills) > 0 {
		b.WriteString("\n\nAgent skills (spawn a sub-agent to perform the task):\n")
		for _, s := range agentSkills {
			fmt.Fprintf(&b, "\n- %s (agent): %s", s.Name, s.Description)
		}
	}
	b.WriteString("\n</system-reminder>")
	return b.String()
}

// ProviderToolAddendum returns a provider-specific addendum that
// reinforces the use of built-in semantic tools over shell commands.
// The addendum is appended to the system prompt at agent creation
// time. It returns an empty string for unknown providers.
func ProviderToolAddendum(provider string) string {
	var shellTool, searchTool string
	switch provider {
	case "anthropic", "claude":
		shellTool, searchTool = "bash", "search_content"
	case "openai", "codex", "llamacpp":
		shellTool, searchTool = "exec_command", "grep_files"
	case "gemini", "antigravity":
		shellTool, searchTool = "run_command", "grep_search"
	default:
		return ""
	}
	return fmt.Sprintf(`

=== CRITICAL: TOOL SELECTION ===
You MUST use Rune's built-in semantic tools instead of shell commands.
Using %s for tasks that have a dedicated tool is INCORRECT and
produces inferior results.

Symbol-aware tools (these understand code structure, not just text).
Reach for them first when you are looking at code:

  1. Given a known symbol name (function, type, variable, method):
     • find_definition     — locate where the symbol is defined.
     • find_references     — list every use site of the symbol.
     • find_implementations — list concrete types that satisfy an
                              interface.
     • describe_symbol     — show the symbol's type signature and
                              doc comment without reading the file.

  2. When the exact symbol name is unknown:
     • search_symbols      — fuzzy search by partial/approximate name.

  3. When you need a file's structure (what it defines):
     • outline_file        — list the top-level symbols. Prefer this
                              over read_file when you only need to
                              know what a file contains.

Only when you are searching for non-symbol text (a literal string, an
error message, a comment, a config value), use %s.

grep/rg/ag invocations through %s may be intercepted and rejected;
the tools above are faster and more accurate for symbol queries.`,
		shellTool, searchTool, shellTool)
}
