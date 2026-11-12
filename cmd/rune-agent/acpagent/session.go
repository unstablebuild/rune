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

package acpagent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	acp "github.com/coder/acp-go-sdk"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agent/taskstore"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
	"unstable.build/rune/internal/debug"
)

const (
	modelOption  acp.SessionConfigId = "model"
	effortOption acp.SessionConfigId = "effort"
	// defaultEffort leaves the reasoning effort to the provider.
	defaultEffort acp.SessionConfigValueId = "default"
)

var efforts = []llmapi.ReasoningEffort{
	llmapi.ReasoningEffortNone,
	llmapi.ReasoningEffortMinimal,
	llmapi.ReasoningEffortLow,
	llmapi.ReasoningEffortMedium,
	llmapi.ReasoningEffortHigh,
	llmapi.ReasoningEffortXHigh,
	llmapi.ReasoningEffortMax,
	llmapi.ReasoningEffortUltra,
}

// session is an ACP session running its own agent.
type session struct {
	a      *Agent
	id     acp.SessionId
	log    *acpapi.Log
	ag     *agent.Agent
	skills *skills.SkillRegistry
	// ctx ends with the session, and with it the running turn.
	ctx  context.Context
	stop context.CancelFunc

	mu sync.Mutex
	// cancel ends the running turn.
	cancel context.CancelFunc
}

func newSession(a *Agent, id acp.SessionId) *session {
	ctx, stop := context.WithCancel(context.Background())
	return &session{a: a, id: id, log: acpapi.NewLog(), ctx: ctx, stop: stop}
}

func (s *session) Info() acpapi.SessionInfo { return acpapi.SessionInfo{ID: s.id} }

func (s *session) Events() iterator.Iterator[acpapi.Event] { return s.log.Events() }

// Prompt runs a turn of the agent. A prompt starting with /<name> of a
// prompt skill runs with that skill loaded.
func (s *session) Prompt(_ context.Context, content []acp.ContentBlock) error {
	message, opts := s.message(content)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.log.Prompt(content); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancel = cancel
	s.a.turns.Add(1)
	go debug.CapturePanicReport(func() {
		defer s.a.turns.Done()
		defer cancel()
		s.log.Done(s.run(ctx, message, opts))
	})
	return nil
}

func (s *session) Cancel(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (s *session) Respond(_ context.Context, id acpapi.PermissionID, o acp.RequestPermissionOutcome) error {
	return s.log.Respond(id, o)
}

// SetConfigOption switches the model, given as provider/name, or the
// reasoning effort.
func (s *session) SetConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) error {
	if req.ValueId == nil {
		return errors.New("acp: rune-agent has no boolean options")
	}
	value := string(req.ValueId.Value)
	switch req.ValueId.ConfigId {
	case modelOption:
		model, err := llmarg.Resolve(ctx, s.a.cfg.LLM, value)
		if err != nil {
			return err
		}
		s.ag.SwapService(s.a.cfg.LLM, model)
	case effortOption:
		effort := llmapi.ReasoningEffort(value)
		switch {
		case req.ValueId.Value == defaultEffort:
			effort = ""
		case !slices.Contains(efforts, effort):
			return fmt.Errorf("acp: unknown effort %q", value)
		}
		s.ag.SetEffort(effort)
	default:
		return fmt.Errorf("acp: option %q: %w", req.ValueId.ConfigId, acpapi.ErrNotFound)
	}
	options, err := s.options(ctx)
	if err != nil {
		return err
	}
	s.log.Update(acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{ConfigOptions: options}})
	return nil
}

// Close ends the session for every viewer and cancels its turn. The
// dialogue stays stored.
func (s *session) Close() error {
	s.mu.Lock()
	s.stop()
	s.log.Close(nil)
	s.mu.Unlock()
	s.a.forget(s)
	return nil
}

// run drives a turn of the agent and says how it ended.
func (s *session) run(ctx context.Context, message string, opts []agent.RunOption) acpapi.TurnEnd {
	it := s.ag.Run(ctx, string(s.id), message, opts...)
	// Close waits for the agent to store the turn.
	defer func() { _ = it.Close() }()
	end := acpapi.TurnEnd{StopReason: acp.StopReasonEndTurn}
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		switch ev.Type {
		case agent.EventText:
			s.log.Update(acp.UpdateAgentMessageText(ev.Text))
		case agent.EventReasoning:
			s.log.Update(acp.UpdateAgentThoughtText(ev.Reasoning))
		case agent.EventToolCall:
			s.log.Update(startToolCall(ev.ToolCallID, ev.ToolName, ev.ToolSummary,
				acp.WithStartStatus(acp.ToolCallStatusInProgress),
				acp.WithStartRawInput(rawInput(ev.ToolArgs))))
		case agent.EventToolResult:
			status := acp.ToolCallStatusCompleted
			if ev.IsError {
				status = acp.ToolCallStatusFailed
			}
			s.log.Update(acp.UpdateToolCall(acp.ToolCallId(ev.ToolCallID),
				acp.WithUpdateStatus(status), acp.WithUpdateContent(textContent(ev.ToolOutput))))
		case agent.EventUsageUpdate:
			if ev.Context.Window > 0 {
				s.log.Update(acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{
					Size: ev.Context.Window,
					Used: ev.Context.TokensSent + ev.Context.TokensReceived,
				}})
			}
		case agent.EventDone:
			// A stop hook can continue the turn after a Done.
			end.StopReason = acp.StopReasonEndTurn
			if ev.FinishReason == llmapi.FinishReasonLength {
				end.StopReason = acp.StopReasonMaxTokens
			}
		case agent.EventRefusal:
			end.StopReason = acp.StopReasonRefusal
		case agent.EventError:
			if end.Error == "" && ev.Error != nil {
				end.Error = ev.Error.Error()
			}
		}
	}
	switch {
	case ctx.Err() != nil:
		return acpapi.TurnEnd{StopReason: acp.StopReasonCancelled}
	case end.Error != "":
		return acpapi.TurnEnd{Error: end.Error}
	}
	return end
}

// message is the agent's message and run options for a prompt.
func (s *session) message(content []acp.ContentBlock) (string, []agent.RunOption) {
	var parts []string
	var images []llmapi.ContentPart
	for _, b := range content {
		switch {
		case b.Text != nil:
			parts = append(parts, b.Text.Text)
		case b.ResourceLink != nil:
			parts = append(parts, "@"+resourcePath(b.ResourceLink.Uri))
		case b.Resource != nil && b.Resource.Resource.TextResourceContents != nil:
			r := b.Resource.Resource.TextResourceContents
			parts = append(parts, fmt.Sprintf("<resource uri=%q>\n%s\n</resource>", r.Uri, r.Text))
		case b.Image != nil:
			images = append(images, llmapi.NewContentPartFromImageURL(
				"data:"+b.Image.MimeType+";base64,"+b.Image.Data))
		}
	}
	message := strings.Join(parts, "\n")
	var opts []agent.RunOption
	if rest, ok := strings.CutPrefix(message, "/"); ok {
		if name := strings.Fields(rest); len(name) > 0 {
			if skill, ok := s.skills.Get(name[0]); ok && skill.Type == "" {
				opts = append(opts, agent.WithSkillName(skill.Name))
			}
		}
	}
	if len(images) > 0 {
		opts = append(opts, agent.WithAttachments(images))
	}
	return message, opts
}

// replay logs a stored dialogue as turns, one per user message.
func (s *session) replay(messages []llmapi.Message) {
	calls := make(map[string]llmapi.ToolCall)
	open := false
	for _, m := range messages {
		switch m.Role {
		case llmapi.RoleUser:
			// Image-only user messages carry tool results.
			if m.Content == "" {
				continue
			}
			if open {
				s.log.Done(acpapi.TurnEnd{StopReason: acp.StopReasonEndTurn})
			}
			open = s.log.Prompt([]acp.ContentBlock{acp.TextBlock(m.Content)}) == nil
		case llmapi.RoleAssistant:
			if m.ReasoningContent != "" {
				s.log.Update(acp.UpdateAgentThoughtText(m.ReasoningContent))
			}
			if m.Content != "" {
				s.log.Update(acp.UpdateAgentMessageText(m.Content))
			}
			for _, c := range m.ToolCalls {
				calls[c.ID] = c
			}
		case llmapi.RoleTool:
			c := calls[m.ToolCallID]
			name := cmp.Or(c.Function.Name, m.Name, "tool")
			s.log.Update(startToolCall(m.ToolCallID, name, "",
				acp.WithStartStatus(acp.ToolCallStatusCompleted),
				acp.WithStartRawInput(rawInput(c.Function.Arguments)),
				acp.WithStartContent(textContent(m.Content))))
		}
	}
	if open {
		s.log.Done(acpapi.TurnEnd{StopReason: acp.StopReasonEndTurn})
	}
}

// options are the session's model and effort selectors.
func (s *session) options(ctx context.Context) ([]acp.SessionConfigOption, error) {
	models, err := iterator.ToSlice(ctx, s.a.cfg.LLM.Models())
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	current := s.ag.ModelEntry()
	qualified := llmarg.Qualify(current)
	if !slices.ContainsFunc(models, func(m llmapi.ModelEntry) bool { return llmarg.Qualify(m) == qualified }) {
		models = append([]llmapi.ModelEntry{current}, models...)
	}
	modelChoices := make(acp.SessionConfigSelectOptionsUngrouped, len(models))
	for i, m := range models {
		modelChoices[i] = acp.SessionConfigSelectOption{
			Value:       acp.SessionConfigValueId(llmarg.Qualify(m)),
			Name:        m.Name,
			Description: new(m.Provider),
		}
	}
	model := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(qualified),
		acp.SessionConfigSelectOptions{Ungrouped: &modelChoices})
	model.Select.Id = modelOption
	model.Select.Name = "Model"
	model.Select.Category = acp.Ptr(acp.SessionConfigOptionCategoryModel)

	effortChoices := acp.SessionConfigSelectOptionsUngrouped{{Value: defaultEffort, Name: "Default"}}
	for _, e := range efforts {
		effortChoices = append(effortChoices, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(e), Name: string(e),
		})
	}
	effort := acp.NewSessionConfigOptionSelect(
		cmp.Or(acp.SessionConfigValueId(s.ag.Effort()), defaultEffort),
		acp.SessionConfigSelectOptions{Ungrouped: &effortChoices})
	effort.Select.Id = effortOption
	effort.Select.Name = "Effort"
	effort.Select.Category = acp.Ptr(acp.SessionConfigOptionCategoryThoughtLevel)
	return []acp.SessionConfigOption{model, effort}, nil
}

// commands offers the prompt skills as slash commands. Agent skills need
// subagents, which the harness leaves out.
func (s *session) commands() acp.SessionUpdate {
	commands := []acp.AvailableCommand{}
	for _, skill := range s.skills.List() {
		if skill.Type != "" {
			continue
		}
		commands = append(commands, acp.AvailableCommand{
			Name:        skill.Name,
			Description: skill.Description,
			Input: &acp.AvailableCommandInput{
				Unstructured: &acp.UnstructuredCommandInput{Hint: "instructions"},
			},
		})
	}
	return acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
		AvailableCommands: commands,
	}}
}

// prompter asks the session's client to approve what a tool is about to
// do. Questions are refused: ACP v1 has no way to ask them.
type prompter struct {
	id  acp.SessionId
	log *acpapi.Log
	n   atomic.Int64
}

func (p *prompter) Prompt(ctx context.Context, req agent.PromptRequest) (agent.PromptResponse, error) {
	if req.Kind != agent.PromptKindPermission {
		return agent.PromptResponse{}, errors.New("acp: rune-agent cannot ask questions over ACP")
	}
	options := make([]acp.PermissionOption, len(req.Options))
	for i, o := range req.Options {
		options[i] = acp.PermissionOption{
			OptionId: acp.PermissionOptionId(o.Value),
			Name:     o.Label,
			Kind:     permissionKind(o.Value),
		}
	}
	// Tools do not know their call ID, so the permission gets a tool call
	// of its own.
	call := acp.ToolCallUpdate{
		ToolCallId: acp.ToolCallId(fmt.Sprintf("permission-%d", p.n.Add(1))),
		Title:      new(req.Title),
		Content:    textContent(req.Body),
	}
	o, err := p.log.Ask(ctx, acp.RequestPermissionRequest{SessionId: p.id, ToolCall: call, Options: options})
	if err != nil {
		return agent.PromptResponse{}, err
	}
	if o.Selected == nil {
		return agent.PromptResponse{}, errors.New("acp: permission cancelled")
	}
	return agent.PromptResponse{Values: []string{string(o.Selected.OptionId)}}, nil
}

// planner reports the agent's tasks as the session's plan.
type planner struct {
	log *acpapi.Log

	mu    sync.Mutex
	tasks []taskstore.Task
}

func (p *planner) UpdateTaskProgress(_ context.Context, task taskstore.Task) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.IndexFunc(p.tasks, func(t taskstore.Task) bool { return t.ID == task.ID })
	switch {
	case task.Status == "deleted":
		if i >= 0 {
			p.tasks = slices.Delete(p.tasks, i, i+1)
		}
	case i >= 0:
		p.tasks[i] = task
	default:
		p.tasks = append(p.tasks, task)
	}
	entries := make([]acp.PlanEntry, len(p.tasks))
	for i, t := range p.tasks {
		status := acp.PlanEntryStatusPending
		switch t.Status {
		case "in_progress":
			status = acp.PlanEntryStatusInProgress
		case "completed":
			status = acp.PlanEntryStatusCompleted
		}
		entries[i] = acp.PlanEntry{Content: t.Subject, Priority: acp.PlanEntryPriorityMedium, Status: status}
	}
	p.log.Update(acp.UpdatePlan(entries...))
}

// permissionKind tells clients what picking an option of a rune-agent
// permission prompt does. Their values are yes, no, always and never, the
// first two optionally scoped to the session as session_yes and
// session_no.
func permissionKind(value string) acp.PermissionOptionKind {
	switch {
	case value == "always":
		return acp.PermissionOptionKindAllowAlways
	case value == "never":
		return acp.PermissionOptionKindRejectAlways
	case value == "no" || strings.HasSuffix(value, "_no"):
		return acp.PermissionOptionKindRejectOnce
	}
	return acp.PermissionOptionKindAllowOnce
}

// startToolCall starts a call of the tool name with summary, which its
// title runs together for any client. Its _meta tells them apart, as
// {"runeAgent": {"toolName": name, "toolSummary": summary}}, so that Rune
// shows the call as Rune Agent's own chat does.
func startToolCall(id, name, summary string, opts ...acp.ToolCallStartOpt) acp.SessionUpdate {
	opts = append([]acp.ToolCallStartOpt{acp.WithStartKind(toolKind(name))}, opts...)
	u := acp.StartToolCall(acp.ToolCallId(id), strings.TrimSpace(name+" "+summary), opts...)
	meta := map[string]any{"toolName": name}
	if summary != "" {
		meta["toolSummary"] = summary
	}
	u.ToolCall.Meta = map[string]any{"runeAgent": meta}
	return u
}

func toolKind(name string) acp.ToolKind {
	switch name {
	case "read_file", "list_dir", "outline_file", "describe_symbol", "list_file_symbols", "check_file_errors":
		return acp.ToolKindRead
	case "apply_patch", "format_file":
		return acp.ToolKindEdit
	case "bash", "exec_command", "write_stdin":
		return acp.ToolKindExecute
	case "search_content", "find_files", "grep_files", "find_definition", "find_references",
		"find_implementations", "search_symbols", "list_symbols", "query_ast", "query_file_ast":
		return acp.ToolKindSearch
	}
	return acp.ToolKindOther
}

// rawInput keeps JSON tool arguments structured for clients.
func rawInput(args string) any {
	switch {
	case args == "":
		return nil
	case json.Valid([]byte(args)):
		return json.RawMessage(args)
	}
	return args
}

func textContent(text string) []acp.ToolCallContent {
	if text == "" {
		return nil
	}
	return []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(text))}
}

// resourcePath is the path of a file:// URI, or the URI itself.
func resourcePath(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		return u.Path
	}
	return uri
}
