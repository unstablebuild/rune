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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
)

func TestCommitAttributionInstructions(t *testing.T) {
	model := llmapi.ModelEntry{Provider: "anthropic", Name: "claude-opus-4-7"}

	t.Run("default uses resolved model", func(t *testing.T) {
		result := CommitAttributionInstructions(model, DefaultAttribution())
		assert.Contains(t, result, "Only create commits when explicitly requested")
		assert.Contains(t, result, "Co-Authored-By: Rune Agent (anthropic/claude-opus-4-7) <agent@rune.build>")
	})

	t.Run("custom text expands model placeholders", func(t *testing.T) {
		custom := "Created-By: {{provider}}/{{model}}"
		result := CommitAttributionInstructions(model, Attribution{Commit: &custom})
		assert.Contains(t, result, "Created-By: anthropic/claude-opus-4-7")
	})

	t.Run("empty text disables attribution", func(t *testing.T) {
		disabled := ""
		result := CommitAttributionInstructions(model, Attribution{Commit: &disabled})
		assert.Empty(t, result)
		assert.NotContains(t, result, "Co-Authored-By")
	})

	t.Run("adds instructions once", func(t *testing.T) {
		result := CommitAttributionInstructions(model, DefaultAttribution())
		assert.Equal(t, 1, strings.Count(result, "Co-Authored-By:"))
	})

	t.Run("unset attribution is omitted", func(t *testing.T) {
		assert.Empty(t, CommitAttributionInstructions(model, Attribution{}))
	})
}

func TestSkillsPromptSection(t *testing.T) {
	t.Run("empty skills returns empty string", func(t *testing.T) {
		assert.Equal(t, "", skillsPromptSection(nil))
	})

	t.Run("builtins include rune-hud as a prompt skill", func(t *testing.T) {
		registry := skills.NewRegistry(nil, workspaceapi.URI{}, nil, nil)
		result := skillsPromptSection(registry.List())
		promptSection, agentSection, _ := strings.Cut(result, "Agent skills")

		assert.Contains(t, promptSection, "- rune-hud: Update Rune's configuration")
		assert.Contains(t, promptSection, "(location: <builtin>/rune-hud/SKILL.md)")
		assert.NotContains(t, agentSection, "rune-hud")
		assert.Contains(t, agentSection, "- explore (agent):")
		assert.Contains(t, agentSection, "- plan (agent):")
		assert.NotContains(t, result, "# Rune HUD images and sounds")
	})

	t.Run("prompt skills only", func(t *testing.T) {
		loaded := []skills.Skill{
			{Name: "debug", Description: "Debug issues", Dir: "/skills/debug"},
		}
		result := skillsPromptSection(loaded)
		assert.Contains(t, result, "- debug: Debug issues")
		assert.NotContains(t, result, "Agent skills")
	})

	t.Run("agent skills separated from prompt skills", func(t *testing.T) {
		loaded := []skills.Skill{
			{Name: "debug", Description: "Debug issues", Dir: "/skills/debug"},
			{Name: "explore", Description: "Explore code", Dir: "/skills/explore", Type: "agent"},
		}
		result := skillsPromptSection(loaded)
		assert.Contains(t, result, "- debug: Debug issues")
		assert.Contains(t, result, "Agent skills (spawn a sub-agent")
		assert.Contains(t, result, "- explore (agent): Explore code")
	})

	t.Run("agent skills only", func(t *testing.T) {
		loaded := []skills.Skill{
			{Name: "explore", Description: "Explore code", Dir: "/skills/explore", Type: "agent"},
		}
		result := skillsPromptSection(loaded)
		assert.Contains(t, result, "Agent skills")
		assert.Contains(t, result, "- explore (agent): Explore code")
	})
}

func TestProviderToolAddendum(t *testing.T) {
	t.Run("anthropic addendum", func(t *testing.T) {
		a := ProviderToolAddendum("anthropic")
		assert.Contains(t, a, "CRITICAL: TOOL SELECTION")
		assert.Contains(t, a, "find_definition")
		assert.Contains(t, a, "find_references")
		assert.Contains(t, a, "search_symbols")
		assert.Contains(t, a, "outline_file")
		assert.Contains(t, a, "search_content")
		assert.Contains(t, a, "bash")
		assert.NotContains(t, a, "grep_files")
	})

	t.Run("openai addendum", func(t *testing.T) {
		a := ProviderToolAddendum("openai")
		assert.Contains(t, a, "CRITICAL: TOOL SELECTION")
		assert.Contains(t, a, "find_definition")
		assert.Contains(t, a, "search_symbols")
		assert.Contains(t, a, "outline_file")
		assert.Contains(t, a, "exec_command")
		assert.Contains(t, a, "grep_files")
		assert.NotContains(t, a, "search_content")
	})

	t.Run("llamacpp addendum", func(t *testing.T) {
		a := ProviderToolAddendum("llamacpp")
		assert.Contains(t, a, "CRITICAL: TOOL SELECTION")
		assert.Contains(t, a, "find_definition")
		assert.Contains(t, a, "exec_command")
		assert.Contains(t, a, "grep_files")
	})

	t.Run("gemini addendum", func(t *testing.T) {
		a := ProviderToolAddendum("gemini")
		assert.Contains(t, a, "CRITICAL: TOOL SELECTION")
		assert.Contains(t, a, "find_definition")
		assert.Contains(t, a, "search_symbols")
		assert.Contains(t, a, "outline_file")
		assert.Contains(t, a, "run_command")
		assert.Contains(t, a, "grep_search")
		assert.NotContains(t, a, "search_content")
	})

	t.Run("unknown provider returns empty", func(t *testing.T) {
		assert.Empty(t, ProviderToolAddendum("ollama"))
		assert.Empty(t, ProviderToolAddendum("unknown"))
		assert.Empty(t, ProviderToolAddendum(""))
	})
}
