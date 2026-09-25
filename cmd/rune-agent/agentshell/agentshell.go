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

package agentshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/audit"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
	"unstable.build/rune/cmd/rune-agent/mcp"
	"unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/workspace/walkdir"
)

// ErrExit is a sentinel error returned by the exit command.
// Pass it to repl.WithExitError so the REPL exits on this error.
var ErrExit = errors.New("exit")

const compactModelAlias = "compact"

// MCPInfo provides a snapshot of MCP server state.
type MCPInfo interface {
	Servers() []*mcp.ServerInfo
}

// Option configures the shell.
type Option func(*shell)

// WithMCPInfo sets the MCP info provider for the mcp command.
func WithMCPInfo(info MCPInfo) Option {
	return func(s *shell) { s.mcpInfo = info }
}

// WithHistorySystemPrompt controls whether the history command includes
// system messages. When false (default), system messages are omitted.
func WithHistorySystemPrompt(show bool) Option {
	return func(s *shell) { s.historySystemPrompt = show }
}

// WithAuditStore sets the audit store for the audit command.
func WithAuditStore(store *audit.Store) Option {
	return func(s *shell) { s.auditStore = store }
}

// WithEffort wires the effort command to a getter/setter pair so that
// the agentshell can display and modify the global default reasoning
// effort that applies to all new chats.
func WithEffort(get func() llmapi.ReasoningEffort, set func(llmapi.ReasoningEffort)) Option {
	return func(s *shell) {
		s.getEffort = get
		s.setEffort = set
	}
}

// WithMaxTokens wires the max_tokens command to a getter/setter pair so the
// embedding extension can apply global config changes to live chats.
func WithMaxTokens(get func() int, set func(int)) Option {
	return func(s *shell) {
		s.getMaxTokens = get
		s.setMaxTokens = set
	}
}

// WithDreamModel sets the model the `dream` command uses when no
// --model flag is given. Without this option the dream command falls
// back to the shell's default model.
func WithDreamModel(model string) Option {
	return func(s *shell) { s.dreamModel = model }
}

// CommandName is the parent REPL command exposed by the agent shell.
const CommandName = "agent"

// New returns a REPL handler backed by the agent shell.
// It panics if wm, llmSvc, skillRegistry, or fs is nil, or if
// memoryDataPath is empty.
func New(
	wm browserapi.WindowManager,
	llmSvc llmapi.Service,
	defaultModel string,
	store dialoguemanager.Store,
	registry *agent.Registry,
	agentsConfig *agent.Cfg,
	cfg configedit.Config,
	skillRegistry *skills.SkillRegistry,
	cwd workspaceapi.URI,
	fs workspaceapi.FileSystem,
	storage storageapi.Service,
	exec workspaceapi.Executor,
	lsp semanticapi.LSP,
	parser syntaxapi.Parser,
	notifications browserapi.Notifications,
	memoryDataPath string,
	opts ...Option,
) textapi.REPLHandler {
	if wm == nil {
		panic("agentshell: WindowManager must not be nil")
	}
	if llmSvc == nil {
		panic("agentshell: llmapi.Service must not be nil")
	}
	if skillRegistry == nil {
		panic("agentshell: SkillRegistry must not be nil")
	}
	if fs == nil {
		panic("agentshell: FileSystem must not be nil")
	}
	if memoryDataPath == "" {
		panic("agentshell: memoryDataPath must not be empty")
	}
	s := &shell{
		wm:                 wm,
		llmSvc:             llmSvc,
		defaultModel:       defaultModel,
		store:              store,
		registry:           registry,
		agentsConfig:       agentsConfig,
		cfg:                cfg,
		skillRegistry:      skillRegistry,
		lastReportedSkills: skillRegistry.Snapshot(),
		cwd:                cwd,
		fs:                 fs,
		storage:            storage,
		exec:               exec,
		lsp:                lsp,
		parser:             parser,
		notifications:      notifications,
		memoryDataPath:     memoryDataPath,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ textapi.REPLHandler = (*shell)(nil)

type shell struct {
	defaultModel        string
	dreamModel          string
	store               dialoguemanager.Store
	registry            *agent.Registry
	agentsConfig        *agent.Cfg
	cfg                 configedit.Config
	mcpInfo             MCPInfo
	wm                  browserapi.WindowManager
	llmSvc              llmapi.Service
	historySystemPrompt bool
	auditStore          *audit.Store
	muSkills            sync.Mutex
	lastReportedSkills  map[string]skills.Skill
	skillRegistry       *skills.SkillRegistry
	cwd                 workspaceapi.URI
	fs                  workspaceapi.FileSystem
	storage             storageapi.Service
	exec                workspaceapi.Executor
	lsp                 semanticapi.LSP
	parser              syntaxapi.Parser
	notifications       browserapi.Notifications
	memoryDataPath      string
	getEffort           func() llmapi.ReasoningEffort
	setEffort           func(llmapi.ReasoningEffort)
	getMaxTokens        func() int
	setMaxTokens        func(int)
}

var commandNames = []string{
	"agents",
	"chats",
	"config",
	"dream",
	"effort",
	"exit",
	"help",
	"mcp",
	"max_tokens",
	"model",
	"models",
	"skills",
	"system-prompt",
	"tools",
}

var commandManual = textapi.CommandManual{
	Name:     CommandName,
	Summary:  "Inspect and manage Rune Agent models, chats, tools, skills, and configuration.",
	Synopsis: "<command> [<args>]",
	Commands: []textapi.CommandManual{
		{Name: "agents", Summary: "List configured agent definitions."},
		{
			Name:     "chats",
			Summary:  "Inspect, export, compact, clear, and fork saved conversations.",
			Synopsis: "(list|show|log|export|clear|compact|fork) [<args>]",
			Commands: []textapi.CommandManual{
				{Name: "list", Summary: "List saved conversations."},
				{Name: "show", Summary: "Show message history for a conversation.", Synopsis: "<id>"},
				{Name: "log", Summary: "Show the LLM token audit log for a conversation.", Synopsis: "<id>"},
				{Name: "export", Summary: "Export a conversation or audit log to a temp file.", Synopsis: "[--audit] <id>"},
				{Name: "clear", Summary: "Clear a conversation and archive its previous contents.", Synopsis: "<id>"},
				{Name: "compact", Summary: "Compact a conversation into a summarized copy using the compact model alias by default.", Synopsis: "<id> [<model>]"},
				{Name: "fork", Summary: "Open a picker to fork a conversation at a selected message.", Synopsis: "<id>"},
			},
		},
		{Name: "config", Summary: "Show current LLM config parameters."},
		{Name: "dream", Summary: "Run memory consolidation on unprocessed dialogues.", Synopsis: "[--model <model>] [--debug]"},
		{Name: "effort", Summary: "Show or set default reasoning effort.", Synopsis: "[none|minimal|low|medium|high|xhigh|max]"},
		{Name: "exit", Summary: "Exit the shell."},
		{Name: "help", Summary: "Show usage for agent commands.", Synopsis: "[<command>...]"},
		{Name: "mcp", Summary: "Show MCP server status and tool stats."},
		{Name: "max_tokens", Summary: "Show or set the global max output tokens config value.", Synopsis: "[<tokens>]"},
		{Name: "model", Summary: "Show the default model or a conversation's assigned model.", Synopsis: "[<dialogue_id>]"},
		{Name: "models", Summary: "List available models with context window sizes."},
		{
			Name:     "skills",
			Summary:  "Inspect discovered skills and configured skill directories.",
			Synopsis: "(list|show|reload|list-dirs|add-dir|remove-dir) [<args>]",
			Commands: []textapi.CommandManual{
				{Name: "list", Summary: "List discovered skills."},
				{Name: "show", Summary: "Show a skill's full instructions.", Synopsis: "<name>"},
				{Name: "reload", Summary: "Re-scan tracked skill directories and report changes."},
				{Name: "list-dirs", Summary: "List configured skill directories."},
				{Name: "add-dir", Summary: "Add a skill directory to config.", Synopsis: "<dir>"},
				{Name: "remove-dir", Summary: "Remove a skill directory from config.", Synopsis: "<dir>"},
			},
		},
		{Name: "system-prompt", Summary: "Show the system prompt for an agent.", Synopsis: "[<agent>]"},
		{Name: "tools", Summary: "List registered agent tools."},
	},
}

// Manual returns the parent REPL command manual for the agent shell.
func Manual() textapi.CommandManual {
	return commandManual
}

func (s *shell) serviceForModel(_ string) (llmapi.Service, error) {
	if s.llmSvc == nil {
		return nil, errors.New("llm service not available")
	}
	return s.llmSvc, nil
}

func subcommandManual(path []string) (textapi.CommandManual, string, bool) {
	man := Manual()
	fullName := man.Name
	for _, name := range path {
		found := false
		for _, child := range man.Commands {
			if child.Name == name {
				man = child
				fullName += " " + child.Name
				found = true
				break
			}
		}
		if !found {
			return textapi.CommandManual{}, "", false
		}
	}
	return man, fullName, true
}

func manualOutput(man textapi.CommandManual, fullName string) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	b.WriteString("## Usage\n\n`")
	b.WriteString(fullName)
	if man.Synopsis != "" {
		b.WriteByte(' ')
		b.WriteString(man.Synopsis)
	}
	b.WriteString("`\n")
	if man.Summary != "" {
		b.WriteString("\n## Description\n\n")
		b.WriteString(man.Summary)
		b.WriteByte('\n')
	}
	if len(man.Commands) > 0 {
		b.WriteString("\n## Subcommands\n\n")
		for _, child := range man.Commands {
			fmt.Fprintf(&b, "- **%s**", child.Name)
			if child.Summary != "" {
				fmt.Fprintf(&b, " — %s", child.Summary)
			}
			b.WriteByte('\n')
		}
	}
	return markdownOutput(b.String())
}

func commandListOutput(man textapi.CommandManual) iterator.Iterator[component.Responsive] {
	var b strings.Builder
	for _, child := range man.Commands {
		b.WriteString("- **")
		b.WriteString(child.Name)
		b.WriteString("**")
		if child.Synopsis != "" {
			b.WriteString(" `")
			b.WriteString(child.Synopsis)
			b.WriteByte('`')
		}
		if child.Summary != "" {
			b.WriteString(" — ")
			b.WriteString(child.Summary)
		}
		b.WriteByte('\n')
	}
	return markdownOutput(b.String())
}

func filterNames(names []string, prefix string) []string {
	var matches []string
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	return matches
}

func (s *shell) completeManualPath(args []string) iterator.Iterator[string] {
	man := Manual()
	if len(args) == 0 {
		return iterator.FromSlice(commandNames)
	}
	for i, arg := range args {
		if i == len(args)-1 {
			var names []string
			for _, child := range man.Commands {
				if strings.HasPrefix(child.Name, arg) {
					names = append(names, child.Name)
				}
			}
			return iterator.FromSlice(names)
		}
		found := false
		for _, child := range man.Commands {
			if child.Name == arg {
				man = child
				found = true
				break
			}
		}
		if !found {
			return iterator.FromSlice[string](nil)
		}
	}
	return iterator.FromSlice[string](nil)
}

func (s *shell) handleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	switch cmd.Name {
	case "help":
		return s.Help(ctx, cmd.Args)
	case "chats":
		return s.handleChats(ctx, cmd.Args)
	case "models":
		return s.listModels(), nil
	case "model":
		return s.model(ctx, cmd.Args)
	case "tools":
		return s.listTools(), nil
	case "agents":
		return s.listAgents(), nil
	case "mcp":
		return s.listMCPServers(), nil
	case "max_tokens":
		return s.handleMaxTokens(cmd.Args)
	case "system-prompt":
		return s.showSystemPrompt(cmd.Args), nil
	case "skills":
		return s.handleSkills(cmd.Args)
	case "config":
		return s.showConfig(), nil
	case "dream":
		return s.handleDream(ctx, cmd.Args, pw)
	case "effort":
		return s.handleEffort(cmd.Args)
	case "exit":
		return s.exit()
	default:
		return nil, fmt.Errorf("unknown command: %s", cmd.Name)
	}
}

// HandleCommand dispatches the given command.
func (s *shell) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if cmd.Name == CommandName {
		if len(cmd.Args) == 0 {
			return s.Help(ctx, nil)
		}
		cmd = repl.Command{Name: cmd.Args[0], Args: cmd.Args[1:]}
	}

	// Re-scan skill directories so out-of-band changes are picked up,
	// mirroring the agent loop's per-turn Reload in agent.go.
	s.skillRegistry.Reload()

	return s.handleCommand(ctx, cmd, pw)
}

func (s *shell) handleChats(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("usage: chats <list|show|log|export|clear|compact|fork> [id]")
	}
	switch args[0] {
	case "list":
		return s.listConversations(ctx)
	case "show":
		if len(args) < 2 {
			return nil, errors.New("usage: chats show <id>")
		}
		return s.showHistory(ctx, args[1])
	case "log":
		if s.auditStore == nil {
			return nil, errors.New("audit log is disabled; set audit_enabled = true in config")
		}
		if len(args) < 2 {
			return nil, errors.New("usage: chats log <id>")
		}
		return s.showAudit(ctx, args[1])
	case "export":
		var audit bool
		var id string
		for _, a := range args[1:] {
			switch a {
			case "--audit":
				audit = true
			default:
				id = a
			}
		}
		if id == "" {
			return nil, errors.New("usage: chats export [--audit] <id>")
		}
		if audit {
			if s.auditStore == nil {
				return nil, errors.New("audit log is disabled; set audit_enabled = true in config")
			}
			return s.exportAudit(ctx, id)
		}
		return s.exportConversation(ctx, id)
	case "clear":
		if len(args) < 2 {
			return nil, errors.New("usage: chats clear <id>")
		}
		return s.clearConversation(ctx, args[1])
	case "compact":
		if len(args) < 2 || len(args) > 3 {
			return nil, errors.New("usage: chats compact <id> [<model>]")
		}
		var model string
		if len(args) == 3 {
			model = args[2]
		}
		return s.compactConversation(ctx, args[1], model)
	case "fork":
		if len(args) < 2 {
			return nil, errors.New("usage: chats fork <id>")
		}
		if len(args) > 2 {
			return nil, errors.New("usage: chats fork <id>")
		}
		return s.forkConversation(ctx, args[1])
	default:
		return nil, fmt.Errorf("unknown chats subcommand: %s", args[0])
	}
}

// Complete provides tab-completion candidates.
func (s *shell) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if cmd == CommandName {
		if len(args) == 0 {
			return iterator.FromSlice[string](nil), nil
		}
		if len(args) == 1 {
			return iterator.FromSlice(filterNames(commandNames, args[0])), nil
		}
		return s.Complete(ctx, args[0], args[1:])
	}

	if len(args) == 0 {
		// Complete command name and skill names.
		var matches []string
		for _, name := range commandNames {
			if strings.HasPrefix(name, cmd) {
				matches = append(matches, name)
			}
		}
		for _, sk := range s.skillRegistry.List() {
			if strings.HasPrefix(sk.Name, cmd) {
				matches = append(matches, sk.Name)
			}
		}
		return iterator.FromSlice(matches), nil
	}

	switch cmd {
	case "help":
		return s.completeManualPath(args), nil
	case "skills":
		return s.completeSkills(ctx, args)
	case "chats":
		return s.completeChats(ctx, args)
	}

	if len(args) > 1 {
		return iterator.FromSlice[string](nil), nil
	}

	switch cmd {
	case "effort":
		return iterator.FromSlice([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}), nil
	case "model":
		return s.completeModelAndDialogueIDs(ctx)
	case "system-prompt":
		return s.completeAgentIDs(), nil
	default:
		return iterator.FromSlice[string](nil), nil
	}
}

// Help returns manual-style help for the agent REPL command hierarchy.
func (s *shell) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man, fullName, ok := subcommandManual(args)
	if !ok {
		return nil, fmt.Errorf("unknown command: %s", strings.Join(args, " "))
	}
	if len(man.Commands) > 0 {
		return commandListOutput(man), nil
	}
	return manualOutput(man, fullName), nil
}

// --- command implementations ---

func (s *shell) listModels() iterator.Iterator[component.Responsive] {
	type entry struct {
		name     string
		ctx      int
		provider string
	}
	it := s.llmSvc.Models()
	defer func() { _ = it.Close() }()
	var entries []entry
	for {
		m, ok := it.Next(context.Background())
		if !ok {
			break
		}
		entries = append(entries, entry{m.Name, m.ContextWindow, m.Provider})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return strings.Compare(a.name, b.name)
	})

	var b strings.Builder
	b.WriteString("## Models\n\n")
	for _, e := range entries {
		marker := ""
		if e.name == s.defaultModel {
			marker = " *(default)*"
		}
		fmt.Fprintf(&b, "- **%s** — %s, %d tokens%s\n", e.name, e.provider, e.ctx, marker)
	}
	return markdownOutput(b.String())
}

func (s *shell) model(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return markdownOutput(s.defaultModel), nil
	}
	d, err := s.store.Get(ctx, args[0])
	if err != nil {
		return nil, fmt.Errorf("get session %q: %w", args[0], err)
	}
	model := d.Model
	if model == "" {
		model = "(not set)"
	}
	return markdownOutput(model), nil
}

func (s *shell) listTools() iterator.Iterator[component.Responsive] {
	tools := s.registry.AllTools()
	slices.SortFunc(tools, func(a, b llmapi.Tool) int {
		return strings.Compare(a.Function.Name, b.Function.Name)
	})
	if len(tools) == 0 {
		return markdownOutput("*(no tools registered)*")
	}
	var b strings.Builder
	b.WriteString("## Tools\n\n")
	for _, t := range tools {
		fmt.Fprintf(&b, "- **%s** — %s\n", t.Function.Name, t.Function.Description)
	}
	return markdownOutput(b.String())
}

func (s *shell) listAgents() iterator.Iterator[component.Responsive] {
	agents := s.agentsConfig.AllowedAgents("default")
	if len(agents) == 0 {
		return markdownOutput("*(no agents configured)*")
	}
	var b strings.Builder
	b.WriteString("## Agents\n\n")
	for _, a := range agents {
		fmt.Fprintf(&b, "- **%s** — %s, model: %s\n", a.ID, a.Name, a.Model)
	}
	return markdownOutput(b.String())
}

func (s *shell) listMCPServers() iterator.Iterator[component.Responsive] {
	if s.mcpInfo == nil {
		return markdownOutput("*(no MCP servers configured)*")
	}

	servers := s.mcpInfo.Servers()
	if len(servers) == 0 {
		return markdownOutput("*(no MCP servers configured)*")
	}

	var b strings.Builder
	b.WriteString("## MCP Servers\n\n")
	for _, srv := range servers {
		status := string(srv.Status)
		if srv.Status == mcp.StatusError && srv.Error != "" {
			status = "error: " + srv.Error
		}

		var callCount, errorCount int64
		for _, name := range srv.ToolNames {
			if ts := srv.ToolStats(name); ts != nil {
				callCount += ts.CallCount()
				errorCount += ts.ErrorCount()
			}
		}

		fmt.Fprintf(&b, "### %s\n\n", srv.Name)
		fmt.Fprintf(&b, "- Status: %s\n", status)
		fmt.Fprintf(&b, "- Command: `%s`\n", srv.Command)
		fmt.Fprintf(&b, "- Tools: %d\n", srv.ToolCount)
		fmt.Fprintf(&b, "- Calls: %d, Errors: %d\n", callCount, errorCount)

		if srv.Status == mcp.StatusConnected && !srv.ConnectedAt.IsZero() {
			fmt.Fprintf(&b, "- Uptime: %s\n", formatDuration(time.Since(srv.ConnectedAt)))
		}
		b.WriteByte('\n')
	}
	return markdownOutput(b.String())
}

func (s *shell) listConversations(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	it, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer it.Close() //nolint:errcheck

	var b strings.Builder
	b.WriteString("## Conversations\n\n")
	var count int
	for {
		d, ok := it.Next(ctx)
		if !ok {
			break
		}
		count++
		fmt.Fprintf(&b, "- **%s** — %d messages, updated %s\n",
			d.ID, d.MessageCount, d.UpdatedAt.Format("2006-01-02 15:04"))
	}
	if it.Err() != nil {
		return nil, it.Err()
	}
	if count == 0 {
		return markdownOutput("*(no conversations)*"), nil
	}
	return markdownOutput(b.String()), nil
}

func (s *shell) showHistory(
	ctx context.Context, id string,
) (iterator.Iterator[component.Responsive], error) {
	d, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get conversation %q: %w", id, err)
	}

	content := formatHistoryMarkdown(d.Messages, s.historySystemPrompt)
	if content == "" {
		return markdownOutput("*(no messages)*"), nil
	}

	cfg := mdConfig()
	md, err := markdown.NewWithConfig(content, cfg)
	if err != nil {
		return nil, fmt.Errorf("create markdown: %w", err)
	}
	mdh := mdhandler.New(md)

	var win browserapi.Window
	bhandler := browserapi.FuncHandler(mdh, func() error {
		return s.wm.CloseWindow(win)
	})
	floating := browserapi.FuncFloating(bhandler, mdh.Dimensions)

	win, err = s.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		return nil, fmt.Errorf("open floating window: %w", err)
	}

	return iterator.Empty[component.Responsive](), nil
}

func formatHistoryMarkdown(msgs []llmapi.Message, includeSystem bool) string {
	if len(msgs) == 0 {
		return ""
	}

	// Build an index of tool call ID → tool name from assistant messages,
	// since tool result messages don't carry the tool name.
	toolNames := make(map[string]string)
	for _, msg := range msgs {
		for _, tc := range msg.ToolCalls {
			toolNames[tc.ID] = tc.Function.Name
		}
	}

	var parts []string
	for _, msg := range msgs {
		var section strings.Builder

		switch msg.Role {
		case llmapi.RoleSystem:
			if !includeSystem {
				continue
			}
			fmt.Fprintf(&section, "> **system:** %s", msg.Content)

		case llmapi.RoleUser:
			section.WriteString("# User\n\n")
			section.WriteString(msg.Content)

		case llmapi.RoleAssistant:
			section.WriteString("# Assistant\n\n")
			if msg.ReasoningContent != "" {
				fmt.Fprintf(&section, "> %s\n\n", msg.ReasoningContent)
			}
			if msg.Content != "" {
				section.WriteString(msg.Content)
			}
			for _, tc := range msg.ToolCalls {
				if section.Len() > 0 && section.String()[section.Len()-1] != '\n' {
					section.WriteByte('\n')
				}
				fmt.Fprintf(&section, "\n**Tool Call:** %s\n```\n%s\n```", tc.Function.Name, tc.Function.Arguments)
			}

		case llmapi.RoleTool:
			name := msg.Name
			if name == "" {
				name = toolNames[msg.ToolCallID]
			}
			if name == "" {
				name = "unknown"
			}
			fmt.Fprintf(&section, "**Tool Result** (%s):\n```\n%s\n```", name, msg.Content)
		}

		if section.Len() > 0 {
			parts = append(parts, section.String())
		}
	}

	return strings.Join(parts, "\n\n---\n\n")
}

func (s *shell) showAudit(
	ctx context.Context, id string,
) (iterator.Iterator[component.Responsive], error) {
	if s.auditStore == nil {
		return markdownOutput("*(no audit store configured)*"), nil
	}
	entries, err := s.auditStore.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get audit log %q: %w", id, err)
	}
	if len(entries) == 0 {
		return markdownOutput("*(no completions recorded)*"), nil
	}

	content := formatAuditMarkdown(entries)
	cfg := mdConfig()
	md, err := markdown.NewWithConfig(content, cfg)
	if err != nil {
		return nil, fmt.Errorf("create markdown: %w", err)
	}
	mdh := mdhandler.New(md)

	var win browserapi.Window
	bhandler := browserapi.FuncHandler(mdh, func() error {
		return s.wm.CloseWindow(win)
	})
	floating := browserapi.FuncFloating(bhandler, mdh.Dimensions)

	win, err = s.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		return nil, fmt.Errorf("open floating window: %w", err)
	}
	return iterator.Empty[component.Responsive](), nil
}

func formatAuditMarkdown(entries []audit.Entry) string {
	var b strings.Builder

	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}

		// Turn header.
		fmt.Fprintf(&b, "## Turn %d", i+1)
		if e.Model != "" {
			fmt.Fprintf(&b, " — %s", e.Model)
		}
		if e.Duration > 0 {
			fmt.Fprintf(&b, " (%s)", formatDuration(e.Duration))
		}
		b.WriteByte('\n')

		// Token stats.
		if e.Err != "" {
			fmt.Fprintf(&b, "\n> **error:** %s\n", e.Err)
		} else {
			fmt.Fprintf(&b, "\n> %d estimated input ∙ %d sent ∙ %d received",
				e.EstimatedTokens, e.Usage.TokensSent, e.Usage.TokensReceived)
			if e.Usage.TokensReasoned > 0 {
				fmt.Fprintf(&b, " ∙ %d reasoning", e.Usage.TokensReasoned)
			}
			if e.Usage.TokensCached > 0 {
				fmt.Fprintf(&b, " ∙ %d cached", e.Usage.TokensCached)
			}
			fmt.Fprintf(&b, " ∙ %s\n", e.FinishReason)
		}

		// Request messages.
		fmt.Fprintf(&b, "\n### Request (%d messages, %d tools)\n", len(e.Messages), e.Tools)

		for _, msg := range e.Messages {
			b.WriteByte('\n')
			formatAuditMessage(&b, msg)
		}

		// Response.
		if e.Response != nil {
			b.WriteString("\n### Response\n\n")
			formatAuditMessage(&b, *e.Response)
		}
	}

	return b.String()
}

func formatAuditMessage(b *strings.Builder, msg llmapi.Message) {
	switch msg.Role {
	case llmapi.RoleSystem:
		fmt.Fprintf(b, "> **system:** %s\n", msg.Content)
	case llmapi.RoleUser:
		fmt.Fprintf(b, "**user:** %s\n", msg.Content)
	case llmapi.RoleAssistant:
		if msg.ReasoningContent != "" {
			fmt.Fprintf(b, "> *reasoning:* %s\n\n", msg.ReasoningContent)
		}
		if msg.Content != "" {
			fmt.Fprintf(b, "**assistant:** %s\n", msg.Content)
		}
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(b, "\n**tool call** %s (`%s`):\n```\n%s\n```\n",
				tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	case llmapi.RoleTool:
		name := msg.Name
		if name == "" {
			name = msg.ToolCallID
		}
		fmt.Fprintf(b, "**tool result** (%s):\n```\n%s\n```\n", name, msg.Content)
	}
}

func (s *shell) exportConversation(
	ctx context.Context, id string,
) (iterator.Iterator[component.Responsive], error) {
	d, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get conversation %q: %w", id, err)
	}
	if len(d.Messages) == 0 {
		return markdownOutput("*(no messages)*"), nil
	}

	f, err := os.CreateTemp("", fmt.Sprintf("conversation-%s-*.md", id))
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var count int
	for _, msg := range d.Messages {
		switch msg.Role {
		case llmapi.RoleUser:
			if count > 0 {
				_, _ = fmt.Fprintln(f)
			}
			_, _ = fmt.Fprintln(f, "## User")
			_, _ = fmt.Fprintln(f)
			_, _ = fmt.Fprintln(f, msg.Content)
			count++
		case llmapi.RoleAssistant:
			if msg.Content == "" {
				continue
			}
			if count > 0 {
				_, _ = fmt.Fprintln(f)
			}
			_, _ = fmt.Fprintln(f, "## Assistant")
			_, _ = fmt.Fprintln(f)
			_, _ = fmt.Fprintln(f, msg.Content)
			count++
		}
	}

	if count == 0 {
		_ = os.Remove(f.Name())
		return markdownOutput("*(no messages)*"), nil
	}

	return markdownOutput(fmt.Sprintf(
		"Exported conversation to `%s`", f.Name(),
	)), nil
}

// auditExportEntry is the per-line JSONL structure for audit export.
// It embeds AuditEntry and adds the dialogue ID and turn number for
// easier correlation when debugging.
type auditExportEntry struct {
	DialogueID string `json:"DialogueID"`
	Turn       int    `json:"Turn"`
	audit.Entry
}

func (s *shell) exportAudit(
	ctx context.Context, id string,
) (iterator.Iterator[component.Responsive], error) {
	entries, err := s.auditStore.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get audit log %q: %w", id, err)
	}
	if len(entries) == 0 {
		return markdownOutput("*(no completions recorded)*"), nil
	}

	f, err := os.CreateTemp("", fmt.Sprintf("audit-%s-*.jsonl", id))
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	for i, e := range entries {
		if err := enc.Encode(auditExportEntry{
			DialogueID: id,
			Turn:       i + 1,
			Entry:      e,
		}); err != nil {
			return nil, fmt.Errorf("write entry %d: %w", i+1, err)
		}
	}

	return markdownOutput(fmt.Sprintf(
		"Exported %d audit entries to `%s`", len(entries), f.Name(),
	)), nil
}

func (s *shell) compactConversation(
	ctx context.Context, id, modelArg string,
) (iterator.Iterator[component.Responsive], error) {
	d, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get conversation %q: %w", id, err)
	}

	if modelArg == "" {
		modelArg = compactModelAlias
	}
	svc, err := s.serviceForModel(modelArg)
	if err != nil {
		return nil, fmt.Errorf("create llm service: %w", err)
	}

	model, err := llmarg.Resolve(ctx, svc, modelArg)
	if err != nil {
		return nil, err
	}
	var sessionMaxTokens int
	if s.getMaxTokens != nil {
		sessionMaxTokens = s.getMaxTokens()
	}
	_, _, err = agent.CompactDialogue(ctx, svc, model, s.store, d,
		agent.WithMaxOutputTokens(agent.SummarizeMaxOutputTokens(sessionMaxTokens, model)),
	)
	if err != nil {
		return nil, fmt.Errorf("compact conversation %q: %w", id, err)
	}
	return iterator.Empty[component.Responsive](), nil
}

func (s *shell) clearConversation(
	ctx context.Context, id string,
) (iterator.Iterator[component.Responsive], error) {
	d, err := s.store.Get(ctx, id)
	if errors.Is(err, storageapi.ErrNotFound) {
		return iterator.Empty[component.Responsive](), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation %q: %w", id, err)
	}
	archivedID, err := agent.NextArchivedID(ctx, s.store, id)
	if err != nil {
		return nil, fmt.Errorf("archive conversation %q: %w", id, err)
	}
	// Keep the system prompt so the conversation remains usable.
	var clearedMsgs []llmapi.Message
	if len(d.Messages) > 0 && d.Messages[0].Role == llmapi.RoleSystem {
		clearedMsgs = []llmapi.Message{d.Messages[0]}
	}
	if err := s.store.ArchiveAndReplace(ctx, dialoguemanager.ArchiveAndReplaceParams{
		Dialogue:           d,
		ArchivedDialogueID: archivedID,
		Messages:           clearedMsgs,
	}); err != nil {
		return nil, fmt.Errorf("clear conversation %q: %w", id, err)
	}
	return markdownOutput(fmt.Sprintf("Cleared **%s** (archived as **%s**)", id, archivedID)), nil
}

func (s *shell) showSystemPrompt(args []string) iterator.Iterator[component.Responsive] {
	agentID := "default"
	if len(args) > 0 {
		agentID = args[0]
	}
	def, ok := s.agentsConfig.Get(agentID)
	if !ok {
		return markdownOutput(fmt.Sprintf("Agent **%s** not found", agentID))
	}
	return markdownOutput(def.SystemPrompt)
}

func (s *shell) showConfig() iterator.Iterator[component.Responsive] {
	keys := []string{
		"base_url", "default_model",
		"temperature", "top_p", "frequency_penalty",
		"presence_penalty", "max_tokens", "max_completion_tokens",
		"reasoning_effort",
	}
	var b strings.Builder
	b.WriteString("## Configuration\n\n")

	// Show per-provider API keys (masked).
	ctx := context.Background()
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		val := ""
		if pcfg, err := s.cfg.GetConfig(provider).Resolve(ctx); err == nil {
			if key, err := pcfg.GetString("api_key").Resolve(ctx); err == nil {
				val = key
			}
		}
		if val != "" {
			if len(val) > 4 {
				val = strings.Repeat("*", len(val)-4) + val[len(val)-4:]
			} else {
				val = "****"
			}
		}
		fmt.Fprintf(&b, "- **%s.api_key**: %s\n", provider, val)
	}

	for _, k := range keys {
		val := configValue(s.cfg, k)
		fmt.Fprintf(&b, "- **%s**: %s\n", k, val)
	}
	return markdownOutput(b.String())
}

func (s *shell) handleMaxTokens(args []string) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		if s.getMaxTokens != nil {
			if n := s.getMaxTokens(); n > 0 {
				return markdownOutput(fmt.Sprintf("Global **max_tokens**: %d", n)), nil
			}
		}
		return markdownOutput(fmt.Sprintf("Global **max_tokens**: %s", configValue(s.cfg, "max_tokens"))), nil
	}
	if len(args) > 1 {
		return nil, errors.New("usage: max_tokens [positive-integer]")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("invalid max_tokens value %q: must be a positive integer", args[0])
	}
	if err := setMaxTokens(context.Background(), s.cfg, n); err != nil {
		return nil, fmt.Errorf("set max_tokens: %w", err)
	}
	if s.setMaxTokens != nil {
		s.setMaxTokens(n)
	}
	return markdownOutput(fmt.Sprintf("Set global **max_tokens** to **%d**.", n)), nil
}

// validEffortLevels lists the allowed reasoning effort values.
var validEffortLevels = []llmapi.ReasoningEffort{
	llmapi.ReasoningEffortNone,
	llmapi.ReasoningEffortMinimal,
	llmapi.ReasoningEffortLow,
	llmapi.ReasoningEffortMedium,
	llmapi.ReasoningEffortHigh,
	llmapi.ReasoningEffortXHigh,
	llmapi.ReasoningEffortMax,
	llmapi.ReasoningEffortUltra,
}

func (s *shell) handleEffort(args []string) (iterator.Iterator[component.Responsive], error) {
	if s.getEffort == nil {
		return nil, errors.New("effort command not available")
	}
	if len(args) == 0 {
		current := s.getEffort()
		if current == "" {
			return markdownOutput("Current effort level: **model default**"), nil
		}
		return markdownOutput(fmt.Sprintf("Current effort level: **%s**", current)), nil
	}

	level := llmapi.ReasoningEffort(args[0])
	valid := false
	for _, v := range validEffortLevels {
		if level == v {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf(
			"invalid effort level %q: must be none, minimal, low, medium, high, xhigh, max, or ultra",
			args[0])
	}

	s.setEffort(level)
	return markdownOutput(fmt.Sprintf("Set effort level to **%s**.", level)), nil
}

func (s *shell) handleSkills(args []string) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("usage: skills <list|show|reload|list-dirs|add-dir|remove-dir> [args]")
	}
	switch args[0] {
	case "list":
		return s.listSkills(), nil
	case "show":
		if len(args) < 2 {
			return nil, errors.New("usage: skills show <name>")
		}
		return s.showSkill(args[1])
	case "reload":
		return s.reloadSkills(), nil
	case "list-dirs":
		return s.listSkillDirs(), nil
	case "add-dir":
		if len(args) < 2 {
			return nil, errors.New("usage: skills add-dir <directory>")
		}
		return s.addSkillDir(args[1])
	case "remove-dir":
		if len(args) < 2 {
			return nil, errors.New("usage: skills remove-dir <directory>")
		}
		return s.removeSkillDir(args[1])
	default:
		return nil, fmt.Errorf("unknown skills subcommand: %s", args[0])
	}
}

func (s *shell) listSkills() iterator.Iterator[component.Responsive] {
	all := s.skillRegistry.List()
	if len(all) == 0 {
		return markdownOutput("*(no skills discovered)*")
	}
	var b strings.Builder
	b.WriteString("## Skills\n\n")
	for _, sk := range all {
		desc := sk.Description
		if len(desc) > 42 {
			desc = desc[:39] + "..."
		}
		fmt.Fprintf(&b, "- **%s** — %s\n", sk.Name, desc)
	}
	return markdownOutput(b.String())
}

func (s *shell) reloadSkills() iterator.Iterator[component.Responsive] {
	s.muSkills.Lock()
	res := s.skillRegistry.ReloadSince(s.lastReportedSkills)
	s.lastReportedSkills = s.skillRegistry.Snapshot()
	s.muSkills.Unlock()
	var b strings.Builder
	b.WriteString("## Skills Reloaded\n\n")

	if len(res.Dirs) > 0 {
		b.WriteString("**Tracked directories:**\n")
		for _, d := range res.Dirs {
			fmt.Fprintf(&b, "- `%s`\n", d)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("*(no skill directories configured)*\n\n")
	}

	hasChanges := len(res.Added) > 0 || len(res.Updated) > 0 || len(res.Dropped) > 0
	if hasChanges {
		if len(res.Added) > 0 {
			fmt.Fprintf(&b, "### Added (%d)\n\n", len(res.Added))
			for _, sk := range res.Added {
				fmt.Fprintf(&b, "- **%s** — %s\n", sk.Name, sk.Description)
			}
			b.WriteString("\n")
		}
		if len(res.Updated) > 0 {
			fmt.Fprintf(&b, "### Updated (%d)\n\n", len(res.Updated))
			for _, sk := range res.Updated {
				fmt.Fprintf(&b, "- **%s** — %s\n", sk.Name, sk.Description)
			}
			b.WriteString("\n")
		}
		if len(res.Dropped) > 0 {
			fmt.Fprintf(&b, "### Dropped (%d)\n\n", len(res.Dropped))
			for _, sk := range res.Dropped {
				fmt.Fprintf(&b, "- **%s**\n", sk.Name)
			}
			b.WriteString("\n")
		}
	} else {
		b.WriteString("No skill changes detected.\n\n")
	}

	if len(res.Errors) > 0 {
		fmt.Fprintf(&b, "### Errors (%d)\n\n", len(res.Errors))
		for _, err := range res.Errors {
			fmt.Fprintf(&b, "- `%s`: %v\n", err.Path, err.Err)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "### Active Skills (%d)\n\n", len(res.Loaded))
	for _, sk := range res.Loaded {
		desc := sk.Description
		if len(desc) > 42 {
			desc = desc[:39] + "..."
		}
		fmt.Fprintf(&b, "- **%s** — %s\n", sk.Name, desc)
	}

	return markdownOutput(b.String())
}

func (s *shell) showSkill(name string) (iterator.Iterator[component.Responsive], error) {
	skill, ok := s.skillRegistry.Get(name)
	if !ok {
		all := s.skillRegistry.List()
		names := make([]string, len(all))
		for i, sk := range all {
			names[i] = sk.Name
		}
		return nil, fmt.Errorf("skill %q not found. Available: %s", name, strings.Join(names, ", "))
	}

	cfg := mdConfig()
	md, err := markdown.NewWithConfig(skill.Body, cfg)
	if err != nil {
		return nil, fmt.Errorf("create markdown: %w", err)
	}
	mdh := mdhandler.New(md)

	var win browserapi.Window
	bhandler := browserapi.FuncHandler(mdh, func() error {
		return s.wm.CloseWindow(win)
	})
	floating := browserapi.FuncFloating(bhandler, mdh.Dimensions)

	win, err = s.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		return nil, fmt.Errorf("open floating window: %w", err)
	}

	return iterator.FromSlice[component.Responsive](nil), nil
}

func (s *shell) listSkillDirs() iterator.Iterator[component.Responsive] {
	dirs := s.skillRegistry.Dirs()
	if len(dirs) == 0 {
		return markdownOutput("*(no skill directories configured)*")
	}
	var b strings.Builder
	b.WriteString("## Skill Directories\n\n")
	for _, d := range dirs {
		fmt.Fprintf(&b, "- `%s`\n", d)
	}
	return markdownOutput(b.String())
}

func (s *shell) addSkillDir(dir string) (iterator.Iterator[component.Responsive], error) {
	if err := addSkillDir(context.Background(), s.cfg, dir); err != nil {
		if errors.Is(err, configedit.ErrAlreadyPresent) {
			return nil, fmt.Errorf("directory %q is already in the skills config", dir)
		}
		return nil, fmt.Errorf("update config: %w", err)
	}
	added, errs, err := s.skillRegistry.AddDir(dir)
	if err != nil {
		return markdownOutput(fmt.Sprintf("Added directory `%s` to config *(registry: %v)*", dir, err)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Added directory `%s` to config\n", dir)
	if len(added) > 0 {
		fmt.Fprintf(&b, "\nDiscovered %d skill(s):\n\n", len(added))
		for _, sk := range added {
			fmt.Fprintf(&b, "- **%s** — %s\n", sk.Name, sk.Description)
		}
	}
	if len(errs) > 0 {
		fmt.Fprintf(&b, "\n### Errors (%d)\n\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(&b, "- `%s`: %v\n", e.Path, e.Err)
		}
	}
	return markdownOutput(b.String()), nil
}

func (s *shell) removeSkillDir(dir string) (iterator.Iterator[component.Responsive], error) {
	if err := removeSkillDir(context.Background(), s.cfg, dir); err != nil {
		if errors.Is(err, configedit.ErrNotPresent) {
			return nil, fmt.Errorf("directory %q is not in the skills config", dir)
		}
		return nil, fmt.Errorf("update config: %w", err)
	}
	s.skillRegistry.RemoveDir(dir) //nolint:errcheck
	return markdownOutput(fmt.Sprintf("Removed directory `%s` from config", dir)), nil
}

func (s *shell) exit() (iterator.Iterator[component.Responsive], error) {
	return nil, ErrExit
}

// --- tab completion helpers ---

func (s *shell) completeChats(ctx context.Context, args []string) (iterator.Iterator[string], error) {
	if len(args) == 1 {
		subcmds := []string{"clear", "compact", "export", "fork", "list", "log", "show"}
		prefix := args[0]
		var matches []string
		for _, sc := range subcmds {
			if strings.HasPrefix(sc, prefix) {
				matches = append(matches, sc)
			}
		}
		return iterator.FromSlice(matches), nil
	}
	if len(args) == 2 {
		switch args[0] {
		case "show", "log", "export", "clear", "compact":
			return s.completeDialogueIDs(ctx)
		case "fork":
			return s.completeDialogueIDs(ctx)
		}
	}
	if len(args) == 3 && args[0] == "compact" {
		return s.completeModels(), nil
	}
	return iterator.FromSlice[string](nil), nil
}

func (s *shell) completeModels() iterator.Iterator[string] {
	return iterator.Map(s.llmSvc.Models(), func(e llmapi.ModelEntry) string {
		return e.Provider + "/" + e.Name
	})
}

func (s *shell) completeDialogueIDs(ctx context.Context) (iterator.Iterator[string], error) {
	it, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	filtered := iterator.Filter(it, func(d dialoguemanager.DialogueHeader) bool {
		return d.ID != ""
	})
	return iterator.Map(filtered, func(d dialoguemanager.DialogueHeader) string {
		return d.ID
	}), nil
}

func (s *shell) completeModelAndDialogueIDs(ctx context.Context) (iterator.Iterator[string], error) {
	modelNames := iterator.Map(s.llmSvc.Models(), func(e llmapi.ModelEntry) string {
		return e.Name
	})
	dialogueIDs, err := s.completeDialogueIDs(ctx)
	if err != nil {
		return modelNames, nil
	}
	return iterator.Aggregate(modelNames, dialogueIDs), nil
}

func (s *shell) completeAgentIDs() iterator.Iterator[string] {
	agents := s.agentsConfig.AllowedAgents("default")
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = a.ID
	}
	return iterator.FromSlice(ids)
}

// --- skills tab completion ---

func (s *shell) completeSkills(ctx context.Context, args []string) (iterator.Iterator[string], error) {
	if len(args) == 1 {
		subcmds := []string{"add-dir", "list", "list-dirs", "reload", "remove-dir", "show"}
		prefix := args[0]
		var matches []string
		for _, sc := range subcmds {
			if strings.HasPrefix(sc, prefix) {
				matches = append(matches, sc)
			}
		}
		return iterator.FromSlice(matches), nil
	}
	if len(args) == 2 {
		switch args[0] {
		case "show":
			return s.completeSkillNames(args[1]), nil
		case "add-dir":
			return s.completeDirs(ctx, args[1])
		case "remove-dir":
			return s.completeSkillDirs(args[1]), nil
		}
	}
	return iterator.FromSlice[string](nil), nil
}

func (s *shell) completeSkillNames(prefix string) iterator.Iterator[string] {
	all := s.skillRegistry.List()
	var matches []string
	for _, sk := range all {
		if strings.HasPrefix(sk.Name, prefix) {
			matches = append(matches, sk.Name)
		}
	}
	return iterator.FromSlice(matches)
}

func (s *shell) completeSkillDirs(prefix string) iterator.Iterator[string] {
	dirs := s.skillRegistry.Dirs()
	var matches []string
	for _, d := range dirs {
		if strings.HasPrefix(d, prefix) {
			matches = append(matches, d)
		}
	}
	return iterator.FromSlice(matches)
}

func (s *shell) completeDirs(ctx context.Context, prefix string) (iterator.Iterator[string], error) {
	root := prefix
	if root == "" {
		root = "."
	}
	return walkdir.ListDirs(ctx, s.fs, root)
}

// --- helpers ---

// --- fork conversation ---

const (
	forkPreviewHeight   = 15
	forkMaxListHeight   = 15
	forkSeparatorHeight = 1
	forkMinWidth        = 80
	forkSpanHPad        = 2
	forkSpanVPad        = 0
)

// forkCandidate describes a selectable message for the fork picker.
type forkCandidate struct {
	messageIndex int
	label        string
	preview      string
}

type forkPickerHandler struct {
	list        *component.FocusList
	candidates  []forkCandidate
	preview     *markdown.Component
	span        *component.Span
	innerW      int
	innerH      int
	previewH    int
	listH       int
	maxEntryW   int
	closeWindow func() error
	onSelect    func(forkCandidate) error
}

type forkPickerInner struct {
	h *forkPickerHandler
}

func (i *forkPickerInner) Dimensions() (int, int) {
	return forkPickerDimensions(len(i.h.candidates), i.h.maxEntryW, forkMinWidth)
}

func (i *forkPickerInner) Resize(w, h int) {
	i.h.innerW = w
	i.h.innerH = h
	i.h.previewH = forkPreviewHeight
	if i.h.previewH > h-1-forkSeparatorHeight {
		i.h.previewH = h - 1 - forkSeparatorHeight
	}
	if i.h.previewH < 0 {
		i.h.previewH = 0
	}
	i.h.listH = h - i.h.previewH - forkSeparatorHeight
	if i.h.listH < 1 {
		i.h.listH = 1
	}
	i.h.preview.Resize(w, i.h.previewH)
	i.h.list.Resize(w, i.h.listH)
}

func (i *forkPickerInner) Draw(w term.Writer) {
	i.h.preview.Draw(w)
	i.h.drawSeparator(w)
	vw := &component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{Y: i.h.previewH + forkSeparatorHeight},
		Width:  i.h.innerW,
		Height: i.h.listH,
	}
	i.h.list.Draw(vw)
}

func newForkPickerHandler(
	candidates []forkCandidate,
	onSelect func(forkCandidate) error,
	closeWindow func() error,
) *forkPickerHandler {
	list := &component.FocusList{}
	list.InitWithAttr(
		term.Attributes{Fg: term.ColorDefault},
		term.Attributes{Fg: term.ColorRed, Attrs: term.AttrBold},
	)
	maxEntryW := 0
	for _, c := range candidates {
		list.PushBack(component.NewResponsiveString(c.label, component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: component.StringConfig{},
		}))
		if w := utf8.RuneCountInString(c.label); w > maxEntryW {
			maxEntryW = w
		}
	}
	if len(candidates) == 0 {
		empty := "(no user or assistant messages to fork from)"
		list.PushBack(component.NewResponsiveString(empty, component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: component.StringConfig{},
		}))
		maxEntryW = max(maxEntryW, utf8.RuneCountInString(empty))
	}
	preview := forkPreviewComponent(candidates, 0)
	h := &forkPickerHandler{
		list:        list,
		candidates:  candidates,
		preview:     preview,
		maxEntryW:   maxEntryW,
		closeWindow: closeWindow,
		onSelect:    onSelect,
	}
	h.span = component.NewSpan(&forkPickerInner{h: h}, component.SpanConfig{
		PadHorizontal:    forkSpanHPad,
		PadVertical:      forkSpanVPad,
		ContentAlignment: component.AlignmentCentered,
	})
	return h
}

func (h *forkPickerHandler) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return false, false
	}
	switch ev.Key {
	case term.KeyEsc:
		return true, true
	case term.KeyEnter:
		if len(h.candidates) == 0 {
			return true, true
		}
		if h.onSelect != nil {
			if err := h.onSelect(h.candidates[h.list.FocusOffset()]); err != nil {
				return true, true
			}
		}
		return true, true
	case term.KeyArrowUp:
		h.list.FocusUp()
		h.updatePreview()
		return false, true
	case term.KeyArrowDown:
		h.list.FocusDown()
		h.updatePreview()
		return false, true
	}
	if ev.Mod == term.ModCtrl {
		switch ev.Ch {
		case 'k', 'p':
			h.list.FocusUp()
			h.updatePreview()
			return false, true
		case 'j', 'n':
			h.list.FocusDown()
			h.updatePreview()
			return false, true
		}
	}
	return false, false
}

func (h *forkPickerHandler) Draw(w term.Writer) {
	h.span.Draw(w)
}

func (h *forkPickerHandler) Resize(w, hgt int) {
	h.span.Resize(w, hgt)
}

func (h *forkPickerHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func (h *forkPickerHandler) Selection() (string, bool) {
	idx := h.list.FocusOffset()
	if idx >= len(h.candidates) {
		return "", false
	}
	return h.candidates[idx].label, true
}

func (h *forkPickerHandler) Close() error {
	if h.closeWindow != nil {
		return h.closeWindow()
	}
	return nil
}

func (h *forkPickerHandler) Dimensions() (int, int) {
	return h.span.Dimensions()
}

func (h *forkPickerHandler) updatePreview() {
	h.preview = forkPreviewComponent(h.candidates, h.list.FocusOffset())
	if h.innerW > 0 || h.innerH > 0 {
		h.preview.Resize(h.innerW, h.previewH)
	}
}

func (h *forkPickerHandler) drawSeparator(w term.Writer) {
	ch := component.FrameCharSetDefault().HorizontalTop
	attr := term.Attributes{Fg: term.ColorGray}
	y := h.previewH
	for x := range h.innerW {
		w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell(ch, 1, attr))
	}
}

func forkPreviewMarkdown(msg llmapi.Message) string {
	preview := strings.TrimSpace(msg.Content)
	if preview != "" {
		return preview
	}
	if len(msg.ToolCalls) > 0 {
		var b strings.Builder
		for i, tc := range msg.ToolCalls {
			if i > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "**Tool Call:** %s\n```\n%s\n```", tc.Function.Name, tc.Function.Arguments)
		}
		return b.String()
	}
	return "*(empty)*"
}

func forkPreviewComponent(candidates []forkCandidate, focus int) *markdown.Component {
	content := "*(no message selected)*"
	if focus >= 0 && focus < len(candidates) {
		content = candidates[focus].preview
	}
	md, err := markdown.NewWithConfig(content, mdConfig())
	if err != nil {
		md, _ = markdown.NewWithConfig("*(preview unavailable)*", mdConfig())
	}
	return md
}

func forkPickerDimensions(count, entryWidth, minWidth int) (int, int) {
	width := max(minWidth, max(entryWidth, forkMinWidth))
	listHeight := count
	if listHeight == 0 {
		listHeight = 1
	}
	listHeight = min(listHeight, forkMaxListHeight)
	return width, forkPreviewHeight + forkSeparatorHeight + listHeight
}

func forkID(dialogueID string) string {
	dialogueID = strings.TrimSuffix(dialogueID, "-fork")
	return dialogueID + "-fork"
}

func nextForkID(ctx context.Context, store dialoguemanager.Store, dialogueID string) (string, error) {
	base := forkID(dialogueID)
	if _, err := store.Get(ctx, base); errors.Is(err, storageapi.ErrNotFound) {
		return base, nil
	} else if err != nil {
		return "", err
	}
	for i := 2; ; i++ {
		candidate := base + "-" + strconv.Itoa(i)
		if _, err := store.Get(ctx, candidate); errors.Is(err, storageapi.ErrNotFound) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

// forkCandidates returns the user/assistant messages from msgs as labelled
// candidates for the fork picker.
func forkCandidates(messages []llmapi.Message) []forkCandidate {
	candidates := make([]forkCandidate, 0, len(messages))
	for i, msg := range messages {
		switch msg.Role {
		case llmapi.RoleUser:
			icon := ""
			candidates = append(candidates, forkCandidate{
				messageIndex: i,
				label:        fmt.Sprintf("%s %s", icon, truncatePreview(messagePreview(msg), 80)),
				preview:      forkPreviewMarkdown(msg),
			})
		case llmapi.RoleAssistant:
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			candidates = append(candidates, forkCandidate{
				messageIndex: i,
				label:        fmt.Sprintf("󰚩 %s", truncatePreview(messagePreview(msg), 80)),
				preview:      forkPreviewMarkdown(msg),
			})
		}
	}
	return candidates
}

// messagePreview returns a single-line preview of a message's content.
func messagePreview(msg llmapi.Message) string {
	preview := strings.TrimSpace(msg.Content)
	if preview == "" && len(msg.ToolCalls) > 0 {
		preview = fmt.Sprintf("(%s tool call)", msg.ToolCalls[0].Function.Name)
	}
	if preview == "" {
		preview = "(empty)"
	}
	preview = strings.ReplaceAll(preview, "\n", " ")
	preview = strings.ReplaceAll(preview, "\t", " ")
	return strings.Join(strings.Fields(preview), " ")
}

// truncatePreview truncates s to max runes, appending "…" if needed.
func truncatePreview(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

// notify sends a notification if the notifications service is available.
func (s *shell) notify(level browserapi.NotificationLevel, msg string, args ...any) {
	if s.notifications == nil {
		return
	}
	_, _ = s.notifications.Notify(level, msg, args...)
}

// forkConversation opens an interactive floating picker showing the
// user/assistant messages of the dialogue. The user can select one
// with Up/Down and press Enter to fork the conversation at that point.
func (s *shell) forkConversation(
	ctx context.Context, id string,
) (iterator.Iterator[component.Responsive], error) {
	d, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get conversation %q: %w", id, err)
	}

	candidates := forkCandidates(d.Messages)

	var win browserapi.Window
	picker := newForkPickerHandler(candidates, func(candidate forkCandidate) error {
		forkedID, err := s.forkDialogue(context.Background(), d, candidate.messageIndex)
		if err != nil {
			s.notify(browserapi.LevelError, "Fork conversation %s: %v", id, err)
			return err
		}
		s.notify(browserapi.LevelSuccess, "Cloned conversation at the selected message. Open %s to resume it.", forkedID)
		return nil
	}, func() error {
		return s.wm.CloseWindow(win)
	})

	win, err = s.wm.Floating(picker, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		return nil, fmt.Errorf("open floating window: %w", err)
	}

	return iterator.Empty[component.Responsive](), nil
}

// forkDialogue creates a new dialogue that is a copy of the given dialogue
// up to and including the message at messageIndex, plus any trailing tool
// result messages.
func (s *shell) forkDialogue(ctx context.Context, d dialoguemanager.Dialogue, messageIndex int) (string, error) {
	if messageIndex < 0 || messageIndex >= len(d.Messages) {
		return "", fmt.Errorf("message index %d out of range [0, %d)", messageIndex, len(d.Messages))
	}

	end := messageIndex + 1
	for end < len(d.Messages) && d.Messages[end].Role == llmapi.RoleTool {
		end++
	}

	newID, err := nextForkID(ctx, s.store, d.ID)
	if err != nil {
		return "", fmt.Errorf("next fork ID for %q: %w", d.ID, err)
	}
	forked := dialoguemanager.Dialogue{
		ID:           newID,
		AgentID:      d.AgentID,
		Model:        d.Model,
		WorkspaceURI: d.WorkspaceURI,
		SubAgent:     d.SubAgent,
		Messages:     slices.Clone(d.Messages[:end]),
	}
	if err := s.store.Create(ctx, forked); err != nil {
		return "", fmt.Errorf("create forked dialogue: %w", err)
	}
	return newID, nil
}

// mdConfig returns a markdown.Config with HeaderPrefix disabled.
func mdConfig() markdown.Config {
	cfg := markdown.DefaultConfig()
	cfg.HeaderPrefix = false
	return cfg
}

func markdownOutput(content string) iterator.Iterator[component.Responsive] {
	cfg := mdConfig()
	md, err := markdown.NewWithConfig(content, cfg)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}

func configValue(cfg configedit.Getter, key string) string {
	ctx := context.Background()
	if v, err := cfg.GetString(key).Resolve(ctx); err == nil {
		return v
	}
	if v, err := cfg.GetFloat(key).Resolve(ctx); err == nil {
		return fmt.Sprintf("%g", v)
	}
	if v, err := cfg.GetInt(key).Resolve(ctx); err == nil {
		return fmt.Sprintf("%d", v)
	}
	return ""
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
