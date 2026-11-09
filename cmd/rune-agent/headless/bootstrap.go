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
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agent/taskstore"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
)

// Host is what rune-agent builds agents from when it does not run as an
// extension.
type Host struct {
	FS       workspaceapi.FileSystem
	Executor workspaceapi.Executor
	LSP      semanticapi.LSP
	Parser   syntaxapi.Parser
	Search   searchapi.Searcher
	Storage  storageapi.Service
	LLM      llmapi.Service
	DataDir  string
}

// NewHost takes the host capabilities from w.
func NewHost(ctx context.Context, w *extensionapi.Workspace) Host {
	return Host{
		FS:       w.FileSystem(ctx),
		Executor: w.Executor(ctx),
		LSP:      w.LSP(ctx),
		Parser:   w.Parser(ctx),
		Search:   w.Search(ctx),
		Storage:  w.Storage(ctx),
		LLM:      w.LLM(ctx),
		DataDir:  w.DataDir(ctx),
	}
}

// AgentConfig configures an agent built by Host.NewAgent.
type AgentConfig struct {
	// Cwd is where the tools resolve relative paths.
	Cwd   workspaceapi.URI
	Store dialoguemanager.Store
	// AgentID tags the dialogues the agent persists.
	AgentID    string
	SessionKey string
	Model      llmapi.ModelEntry
	// Prompter asks the user to approve tool calls. Nil lets them run.
	Prompter agent.Prompter
	// Progress receives the task tools' updates. Nil leaves the task tools
	// out.
	Progress agent.ProgressUpdater
}

// Built is an agent and the registries it runs with.
type Built struct {
	Agent    *agent.Agent
	Registry *agent.Registry
	Skills   *skills.SkillRegistry
}

// NewAgent builds an agent from the host capabilities.
//
// Everything the interactive extension installs for the sake of a user
// interface — terminal, notifications, editor, window manager,
// interrupter, command registration, MCP, hooks, the skill tool,
// question tools, web_browser and subagents — is deliberately absent so
// the agent has no side effects on the live session.
func (h Host) NewAgent(cfg AgentConfig) Built {
	// The host strips the `extensions` key before serving its config
	// over RPC, so the extension's own config block is unreachable
	// here; a nil snapshot keeps apply_patch able to edit
	// .rune/config.yaml while resolving reads through the overlay only.
	conf := configedit.NewConfig(h.FS, cfg.Cwd, nil)
	tools, tracker := agentools.DefaultTools(
		h.FS, h.Executor, cfg.Cwd, h.LSP, h.Search, agentools.Config{}, conf)
	tools = append(tools, agentools.LSPTools(h.LSP, h.FS, h.Parser, cfg.Cwd, tracker)...)
	tools = append(tools, agentools.SyntaxTools(h.Parser, h.FS, cfg.Cwd, tracker)...)
	if cfg.Progress != nil {
		tools = append(tools, agentools.NewTaskTools(taskstore.New(), cfg.Progress)...)
	}
	registry := agent.NewRegistry(tools...)
	if cfg.Progress != nil {
		for _, provider := range []string{"openai", "codex"} {
			registry.RegisterOverrides(provider, agentools.NewUpdatePlan(cfg.Progress))
			registry.RegisterExclusions(provider, "TaskCreate", "TaskUpdate", "TaskGet", "TaskList")
		}
	}

	agentsFiles := agent.DiscoverAgentsFiles(h.FS, cfg.Cwd.Path(), agent.DefaultAgentsFile)
	skillRegistry := skills.NewRegistry(h.FS, cfg.Cwd, nil, nil)
	ag := agent.NewAgent(h.LLM, registry, skillRegistry, cfg.Store, agent.NoMemory(),
		agent.Config{
			SystemPrompt:        agent.DefaultSystemPrompt(cfg.Cwd),
			Attribution:         agent.DefaultAttribution(),
			ProjectInstructions: agent.LoadAgentsFiles(h.FS, agentsFiles),
			Model:               cfg.Model,
			Workspace:           cfg.Cwd,
			AgentID:             cfg.AgentID,
			SessionKey:          cfg.SessionKey,
			Prompter:            cfg.Prompter,
		})
	return Built{Agent: ag, Registry: registry, Skills: skillRegistry}
}

// session holds everything a headless run needs after bootstrap.
type session struct {
	agent    *agent.Agent
	registry *agent.Registry
	model    llmapi.ModelEntry
	cwd      workspaceapi.URI
	// cleanup removes the run-scoped dialogue directory.
	cleanup func()
}

// bootstrap builds the agent of a headless run. It neither prompts nor
// tracks tasks, so the run behaves deterministically.
func bootstrap(
	ctx context.Context, w *extensionapi.Workspace, opts Options, runID string,
) (*session, error) {
	host := NewHost(ctx, w)

	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	cwd, err := host.FS.URI(wd)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory %q: %w", wd, err)
	}

	model, err := llmarg.Resolve(ctx, host.LLM, opts.Model)
	if err != nil {
		return nil, fmt.Errorf("resolve model %q: %w", opts.Model, err)
	}

	sessionsDir := filepath.Join(host.DataDir, "headless", runID)
	built := host.NewAgent(AgentConfig{
		Cwd:        cwd,
		Store:      dialoguemanager.NewStore(host.Storage, sessionsDir),
		AgentID:    ExtensionID,
		SessionKey: runID,
		Model:      model,
	})
	built.Agent.SetEffort(llmapi.ReasoningEffort(opts.Effort))

	return &session{
		agent:    built.Agent,
		registry: built.Registry,
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
