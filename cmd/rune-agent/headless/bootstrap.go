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

package headless

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
)

// session holds everything a headless run needs after bootstrap.
type session struct {
	agent    *agent.Agent
	registry *agent.Registry
	model    llmapi.ModelEntry
	cwd      workspaceapi.URI
	// cleanup removes the run-scoped dialogue directory.
	cleanup func()
}

// bootstrap builds the agent graph from host capabilities.
//
// Everything the interactive extension installs for the sake of a user
// interface — terminal, notifications, editor, window manager,
// interrupter, command registration, MCP, hooks, skills, web_fetch and
// subagents — is deliberately absent so a headless run has no side
// effects on the live session and behaves deterministically.
func bootstrap(
	ctx context.Context, w *extensionapi.Workspace, opts Options, runID string,
) (*session, error) {
	fs := w.FileSystem(ctx)
	executor := w.Executor(ctx)
	lsp := w.LSP(ctx)
	parser := w.Parser(ctx)
	storage := w.Storage(ctx)
	llmSvc := w.LLM(ctx)
	dataDir := w.DataDir(ctx)

	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	cwd, err := fs.URI(wd)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory %q: %w", wd, err)
	}

	model, err := llmarg.Resolve(ctx, llmSvc, opts.Model)
	if err != nil {
		return nil, fmt.Errorf("resolve model %q: %w", opts.Model, err)
	}

	// The host strips the `extensions` key before serving its config
	// over RPC, so the extension's own config block is unreachable
	// here; a nil snapshot keeps apply_patch able to edit
	// .rune/config.yaml while resolving reads through the overlay only.
	cfg := configedit.NewConfig(fs, cwd, nil)
	tools, tracker := agentools.DefaultTools(
		fs, executor, cwd, lsp, parser, agentools.Config{}, cfg)
	tools = append(tools, agentools.LSPTools(lsp, fs, parser, cwd, tracker)...)
	tools = append(tools, agentools.SyntaxTools(parser, fs, cwd, tracker)...)
	registry := agent.NewRegistry(tools...)

	systemPrompt := agent.DefaultSystemPrompt(cwd)
	agentsFiles := agent.DiscoverAgentsFiles(fs, cwd.Path(), agent.DefaultAgentsFile)

	sessionsDir := filepath.Join(dataDir, "headless", runID)
	store := dialoguemanager.NewStore(storage, sessionsDir)

	ag := agent.NewAgent(llmSvc, registry,
		skills.NewRegistry(fs, cwd, nil, nil), store, agent.NoMemory(),
		agent.Config{
			SystemPrompt:        systemPrompt,
			Attribution:         agent.DefaultAttribution(),
			ProjectInstructions: agent.LoadAgentsFiles(fs, agentsFiles),
			Model:               model,
			Workspace:           cwd,
			AgentID:             ExtensionID,
			SessionKey:          runID,
		})
	ag.SetEffort(llmapi.ReasoningEffort(opts.Effort))

	return &session{
		agent:    ag,
		registry: registry,
		model:    model,
		cwd:      cwd,
		cleanup: func() {
			if err := os.RemoveAll(sessionsDir); err != nil {
				slog.Warn("remove headless session directory",
					"dir", sessionsDir, "error", err)
			}
		},
	}, nil
}
