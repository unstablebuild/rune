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
	"log/slog"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace/walkdir"
)

// Config holds optional configuration for the default tool set.
type Config struct {
	// MaxLineBytes is the per-line truncation limit for read_file output.
	// 0 uses agent.DefaultMaxLineBytes.
	MaxLineBytes int
}

// DefaultTools returns all available tools pre-configured with the
// workspace working directory and the shared FileTracker that should
// be passed to LSPTools and SyntaxTools.
//
// cfg is mandatory: the bash tool needs it to intercept bare grep
// invocations (read force_builtin_tools, prompt the user, persist
// "Always"/"Never" choices). Tests that have no workspace
// configuration should pass configedit.NopConfig().
func DefaultTools(
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	cwd workspaceapi.URI,
	lsp semanticapi.LSP,
	parser syntaxapi.Parser,
	toolsCfg Config,
	cfg configedit.Config,
) ([]agent.Tool, *FileTracker) {
	if cfg == nil {
		panic("agentools.DefaultTools: cfg must not be nil; pass configedit.NopConfig() in tests")
	}
	tracker := NewFileTracker()
	// Match the IDE fuzzy finder: skip .gitignore'd and swap files in the
	// agent's file-walking tools. Degrade to no filter on schemes without a
	// gitignore (e.g. in-memory docs:///).
	var ignore walkdir.Filter
	if m, err := vctrl.LoadGitignore(fs); err != nil {
		slog.Warn("agentools: load gitignore matcher", "error", err)
	} else {
		ignore = m
	}
	return []agent.Tool{
		newReadFile(fs, cwd, tracker, toolsCfg.MaxLineBytes),
		newApplyPatch(fs, cwd, tracker, lsp),
		newSearch(fs, cwd, tracker, ignore, lsp, parser),
		newFindFiles(fs, cwd, tracker, ignore),
		newBash(exec, cwd, cfg),
		newCompact(),
	}, tracker
}

// SessionTools returns session management tools for a
// parent agent session. The agents slice is embedded in
// the agent tool's description. skillRegistry adds
// agent-type skills to the description. childEvents
// receives sub-agent events for TUI rendering. Sub-agents
// do NOT receive these tools.
func SessionTools(
	spawner agent.Spawner, agents []agent.AgentSummary,
	childEvents chan<- agent.ChildEvent,
	skillRegistry *skills.SkillRegistry,
) []agent.Tool {
	return []agent.Tool{
		NewAgentTool(spawner, agents, childEvents, skillRegistry),
	}
}

// MemoryTools returns tools related to the memory system.
// Only registered when the memory workspace is available.
func MemoryTools(exec workspaceapi.Executor, memoryPath string) []agent.Tool {
	return []agent.Tool{NewRecallConversation(exec, memoryPath)}
}

// ConversationTools returns tools for introspecting stored conversations.
// Always registered; tools fail gracefully if the store is empty.
func ConversationTools(store dialoguemanager.Store, fs walkdir.Reader, sessionsDir string) []agent.Tool {
	return []agent.Tool{
		&listConversationsTool{store: store, sessionsDir: sessionsDir},
		&searchConversationsTool{fs: fs, sessionsDir: sessionsDir},
	}
}
