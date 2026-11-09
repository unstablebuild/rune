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

// Package acpagent serves rune-agent's harness as an Agent Client Protocol
// agent, so any ACP client can run it.
package acpagent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
)

// Config configures an Agent.
type Config struct {
	// Info names the agent to clients.
	Info acp.Implementation
	// LLM resolves Model and lists the models sessions can switch to.
	LLM llmapi.Service
	// Store keeps the sessions' dialogues. ListSessions lists every
	// top-level dialogue in it, so it should hold only this agent's.
	Store dialoguemanager.Store
	// Model is the model argument sessions start with. Empty is
	// llmapi.DefaultModel.
	Model string
	// NewAgent builds the agent of session req.ID. The agent must keep its
	// dialogue in Store under req.ID, ask for permissions through
	// req.Prompter and report task progress to req.Progress. The prompt
	// skills of the returned registry are offered as slash commands.
	NewAgent func(ctx context.Context, req AgentRequest) (*agent.Agent, *skills.SkillRegistry, error)
}

// AgentRequest describes the session an agent is built for.
type AgentRequest struct {
	ID string
	// Cwd is the session's absolute working directory.
	Cwd      string
	Model    llmapi.ModelEntry
	Prompter agent.Prompter
	Progress agent.ProgressUpdater
}

// Agent is rune-agent's harness as an acpapi.Agent. Every session runs its
// own agent and keeps its dialogue in Config.Store, so it can be loaded
// again by a later process.
type Agent struct {
	cfg   Config
	done  chan struct{}
	turns sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	sessions map[acp.SessionId]*session
}

var _ acpapi.Agent = (*Agent)(nil)

// New returns an Agent that runs sessions as cfg says.
func New(cfg Config) *Agent {
	return &Agent{
		cfg:      cfg,
		done:     make(chan struct{}),
		sessions: make(map[acp.SessionId]*session),
	}
}

// Info advertises session loading, listing and closing, and images and
// embedded resources in prompts.
func (a *Agent) Info() acp.InitializeResponse {
	info := a.cfg.Info
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &info,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			SessionCapabilities: acp.SessionCapabilities{
				List:  &acp.SessionListCapabilities{},
				Close: &acp.SessionCloseCapabilities{},
			},
		},
	}
}

// NewSession starts a session whose events open with its config options
// and slash commands.
func (a *Agent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acpapi.Session, error) {
	return a.open(ctx, uuid.NewString(), req.Cwd, nil)
}

// LoadSession resumes a stored session. Its events replay the dialogue,
// one turn per user message, before the config options and commands.
func (a *Agent) LoadSession(ctx context.Context, req acp.LoadSessionRequest) (acpapi.Session, error) {
	d, err := a.cfg.Store.Get(ctx, string(req.SessionId))
	if errors.Is(err, storageapi.ErrNotFound) {
		return nil, fmt.Errorf("acp: session %s: %w", req.SessionId, acpapi.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", req.SessionId, err)
	}
	return a.open(ctx, d.ID, req.Cwd, d.Messages)
}

// ListSessions lists the stored sessions, most recently updated first.
func (a *Agent) ListSessions(ctx context.Context, req acp.ListSessionsRequest) iterator.Iterator[acp.SessionInfo] {
	it, err := a.cfg.Store.List(ctx)
	if err != nil {
		return iterator.Error[acp.SessionInfo](fmt.Errorf("list sessions: %w", err))
	}
	// Compaction archives the replaced history as a dialogue of its own.
	it = iterator.Filter(it, func(h dialoguemanager.DialogueHeader) bool {
		return !h.SubAgent && !strings.Contains(h.ID, "-archived")
	})
	infos := iterator.Map(it, func(h dialoguemanager.DialogueHeader) acp.SessionInfo {
		info := acp.SessionInfo{
			SessionId: acp.SessionId(h.ID),
			UpdatedAt: new(h.UpdatedAt.UTC().Format(time.RFC3339)),
		}
		if ws, ok := h.Workspace(); ok {
			info.Cwd = ws.Path()
		}
		return info
	})
	return iterator.Filter(infos, func(info acp.SessionInfo) bool {
		return req.Cwd == nil || *req.Cwd == info.Cwd
	})
}

// Done is closed by Close.
func (a *Agent) Done() <-chan struct{} { return a.done }

// Err is always nil: the agent runs in process.
func (a *Agent) Err() error { return nil }

// Close closes every session and waits for their turns to end, so their
// dialogues are stored.
func (a *Agent) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	sessions := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
	a.turns.Wait()
	close(a.done)
	return nil
}

func (a *Agent) open(ctx context.Context, id, cwd string, history []llmapi.Message) (*session, error) {
	if !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("acp: cwd %q is not an absolute path", cwd)
	}
	model, err := llmarg.Resolve(ctx, a.cfg.LLM, cmp.Or(a.cfg.Model, llmapi.DefaultModel))
	if err != nil {
		return nil, fmt.Errorf("resolve model: %w", err)
	}
	s := newSession(a, acp.SessionId(id))
	s.ag, s.skills, err = a.cfg.NewAgent(ctx, AgentRequest{
		ID:       id,
		Cwd:      cwd,
		Model:    model,
		Prompter: &prompter{id: s.id, log: s.log},
		Progress: &planner{log: s.log},
	})
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}
	options, err := s.options(ctx)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	switch {
	case a.closed:
		a.mu.Unlock()
		return nil, acpapi.ErrClosed
	case a.sessions[s.id] != nil:
		a.mu.Unlock()
		return nil, fmt.Errorf("acp: session %s: %w", s.id, acpapi.ErrExists)
	}
	a.sessions[s.id] = s
	a.mu.Unlock()

	s.replay(history)
	s.log.Started(acpapi.Started{ConfigOptions: options})
	s.log.Update(s.commands())
	return s, nil
}

func (a *Agent) forget(s *session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[s.id] == s {
		delete(a.sessions, s.id)
	}
}
