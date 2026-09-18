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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent/audit"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agent/utf8validate"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/hooks"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
)

// Config holds agent configuration.
type Config struct {
	MaxIterations      int
	MaxToolOutputBytes int     // 0 uses DefaultMaxToolOutputBytes.
	AutoCompactRatio   float64 // 0 uses defaultAutoCompactRatio.
	SystemPrompt       string
	Attribution        Attribution
	SessionKey         string
	AgentID            string
	// Model carries the fully-resolved entry the agent loop targets.
	// Name, Provider, and ContextWindow are all consulted by the loop;
	// callers obtain a populated ModelEntry by routing through
	// llmapi.Service.GetModel before constructing the Config.
	Model     llmapi.ModelEntry
	Workspace workspaceapi.URI
	SubAgent  bool // true for sub-agent dialogues spawned by agent tool calls.

	// CompactSvc, when non-nil, is used for summarization during
	// compaction instead of the agent's own LLM service. This allows
	// using a cheaper/faster model for conversation summaries.
	CompactSvc llmapi.Service

	// ProjectInstructions holds the content loaded from project
	// instruction files (e.g. AGENTS.md). When non-empty it is
	// injected as a <project-instructions> XML block prepended to
	// the user message on every turn (not persisted).
	ProjectInstructions string

	// Hooks, when non-nil, dispatches Claude-Code-style hooks at
	// well-defined points in the agent loop (SessionStart,
	// PostToolUse, Stop, PreCompact, etc.). A nil runner is a no-op.
	Hooks *hooks.Runner

	// Prompter, when non-nil, is propagated to tool executions through
	// the context so that tools (e.g. bash) can block and ask the user
	// for input. A nil prompter is allowed; tools that need user input
	// must handle this case (typically by allowing the operation or
	// returning an error).
	Prompter Prompter
}

// Memory represents a single recalled memory entry.
type Memory struct {
	ID      string // e.g. "recent-iterator-bug"
	Content string // the memory knowledge text
}

// MemoryRecaller retrieves relevant memories for the current context.
// Implementations should return (nil, nil) when no memories match.
type MemoryRecaller interface {
	Recall(ctx context.Context, files []string, task string, workspaceRoot string) ([]Memory, error)
}

// NoMemory returns a MemoryRecaller that always reports no memories.
// Use it for agents that have no memory system configured.
func NoMemory() MemoryRecaller { return noMemory{} }

type noMemory struct{}

func (noMemory) Recall(_ context.Context, _ []string, _ string, _ string) ([]Memory, error) {
	return nil, nil
}

// Agent orchestrates the agentic loop: LLM → tool call → result → repeat.
type Agent struct {
	mu              sync.Mutex // protects svc, config.Model, effort, and maxOutputTokens
	svc             llmapi.Service
	registry        *Registry
	skillRegistry   *skills.SkillRegistry
	store           dialoguemanager.Store
	resources       sync.Map
	config          Config
	effort          llmapi.ReasoningEffort // session-level effort override
	maxOutputTokens int                    // session-level max-output-token override
	memory          MemoryRecaller
	// autoDiagDisabled records language ids (keyed by string) for which
	// auto-injected check_file_errors has been observed to fail because no
	// LSP is running for that language. Once disabled, auto-diagnostics is
	// skipped for that language for the rest of the session.
	autoDiagDisabled sync.Map
}

// SwapService replaces the LLM service and model used by the agent.
// It must be called between Run invocations, not during one.
func (a *Agent) SwapService(svc llmapi.Service, model llmapi.ModelEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.svc = svc
	a.config.Model = model
}

// provider returns the current provider name under the mutex.
func (a *Agent) provider() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.Model.Provider
}

// Model returns the current model name.
func (a *Agent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.Model.Name
}

// Provider returns the current provider name.
func (a *Agent) Provider() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.Model.Provider
}

// ModelEntry returns the full ModelEntry the agent is currently bound to.
func (a *Agent) ModelEntry() llmapi.ModelEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.Model
}

// Hooks returns the hook runner configured for this agent. NewAgent
// guarantees a non-nil runner: a runner with no configured hooks is
// a no-op.
func (a *Agent) Hooks() *hooks.Runner {
	return a.config.Hooks
}

// Workspace returns the workspace URI the agent is bound to. Callers
// must treat this as a potentially non-local URI (e.g. ssh://) — it
// is not safe to feed straight into local filesystem APIs.
func (a *Agent) Workspace() workspaceapi.URI {
	return a.config.Workspace
}

// SetEffort sets the session-level reasoning effort. An empty string
// means use the provider/config default.
func (a *Agent) SetEffort(effort llmapi.ReasoningEffort) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.effort = effort
}

// Effort returns the current session-level reasoning effort.
func (a *Agent) Effort() llmapi.ReasoningEffort {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effort
}

// SetMaxOutputTokens sets the session-level max-output-token override.
// A non-positive value means use the provider/config default.
func (a *Agent) SetMaxOutputTokens(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.maxOutputTokens = n
}

// MaxOutputTokens returns the current session-level max-output-token override.
func (a *Agent) MaxOutputTokens() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxOutputTokens
}

// NewAgent creates a new Agent. Panics if skillRegistry is nil.
func NewAgent(
	svc llmapi.Service,
	registry *Registry,
	skillRegistry *skills.SkillRegistry,
	store dialoguemanager.Store,
	memory MemoryRecaller,
	config Config,
) *Agent {
	if skillRegistry == nil {
		panic("agent: skill registry must not be nil")
	}
	if memory == nil {
		panic("agent: memory must not be nil")
	}
	if config.MaxIterations <= 0 {
		config.MaxIterations = 500
	}
	if config.Hooks == nil {
		// Auto-initialize so call sites can dispatch hooks
		// unconditionally; the empty config produces a no-op runner.
		config.Hooks = hooks.NewRunner(hooks.Config{}, nil, nil, "")
	}
	return &Agent{
		svc:           svc,
		registry:      registry,
		skillRegistry: skillRegistry,
		store:         store,
		memory:        memory,
		config:        config,
	}
}

// AddContextResource adds an editor resource to the agent's context.
func (a *Agent) AddContextResource(
	ctx context.Context, uri workspaceapi.URI, data string,
) error {
	a.resources.Store(uri, data)
	return nil
}

// RemoveContextResource removes an editor resource from the agent's context.
func (a *Agent) RemoveContextResource(
	ctx context.Context, uri workspaceapi.URI,
) error {
	_, loaded := a.resources.LoadAndDelete(uri)
	if !loaded {
		return fmt.Errorf("resource with URI %q not found", uri.String())
	}
	return nil
}

// resourceFiles extracts file paths from the agent's context resources.
func (a *Agent) resourceFiles() []string {
	var files []string
	a.resources.Range(func(k, _ any) bool {
		if uri, ok := k.(workspaceapi.URI); ok {
			files = append(files, uri.Path())
		}
		return true
	})
	return files
}

// RunOption configures optional parameters for Agent.Run.
type RunOption func(*runOptions)

type runOptions struct {
	skillName         string
	toolCallResults   []ToolCallResult
	attachments       []llmapi.ContentPart
	displayMessage    string
	displaySet        bool
	additionalContext string
}

// ToolCallResult represents a pre-computed tool call result that is
// injected into the message history before the first LLM call. This
// avoids redundant tool invocations when the caller already knows
// what files the agent will read.
type ToolCallResult struct {
	ToolName  string
	Arguments string
	Content   string
}

type toolCallInfo struct {
	call    llmapi.ToolCall
	tool    Tool
	found   bool
	summary string
}

type executedToolCall struct {
	index    int
	info     toolCallInfo
	result   ToolResult
	duration time.Duration
}

// emptyToolResultPlaceholder stands in for a tool result that produced no
// textual output. Anthropic rejects an empty tool_result text block with
// "text content blocks must be non-empty", so the replayed content must
// always carry at least this marker.
const emptyToolResultPlaceholder = "(tool produced no output)"

// nonEmptyToolResult guarantees a tool-role message never carries empty
// content. A tool that writes nothing to stdout/stderr (e.g. `touch`)
// otherwise yields an empty tool_result that the provider rejects mid-turn.
func nonEmptyToolResult(content string) string {
	if content == "" {
		return emptyToolResultPlaceholder
	}
	return content
}

// WithSkillName sets the skill pre-loaded via slash command.
func WithSkillName(name string) RunOption {
	return func(o *runOptions) {
		o.skillName = name
	}
}

// WithToolCallResults injects pre-computed tool call results into the
// message history. The results appear as if the agent had already
// called these tools before its first LLM turn.
func WithToolCallResults(results []ToolCallResult) RunOption {
	return func(o *runOptions) {
		o.toolCallResults = results
	}
}

// WithAttachments sends the given content parts alongside the user
// message, carrying files the user attached to the chat.
func WithAttachments(parts []llmapi.ContentPart) RunOption {
	return func(o *runOptions) {
		o.attachments = parts
	}
}

// WithDisplayMessage sets the human-readable text persisted in Message.Content.
// The message passed to Run remains the model-facing text.
func WithDisplayMessage(message string) RunOption {
	return func(o *runOptions) {
		o.displayMessage = message
		o.displaySet = true
	}
}

// WithAdditionalContext prepends transient context to the provider request
// without persisting it in the dialogue.
func WithAdditionalContext(context string) RunOption {
	return func(o *runOptions) {
		o.additionalContext = context
	}
}

func prependMessageText(msg llmapi.Message, prefix string) llmapi.Message {
	if prefix == "" {
		return msg
	}
	if len(msg.MultiContent) > 0 &&
		msg.MultiContent[0].Type == llmapi.ContentPartTypeText {
		msg.MultiContent = slices.Clone(msg.MultiContent)
		msg.MultiContent[0].Text = prefix + "\n\n" + msg.MultiContent[0].Text
		return msg
	}
	msg.Content = prefix + "\n\n" + msg.Content
	return msg
}

// Run executes the agentic loop for the given dialogue and user message.
// It returns an iterator of Events that the caller consumes to drive the UI.
func (a *Agent) Run(
	ctx context.Context, dialogueID string, message string, opts ...RunOption,
) iterator.Iterator[Event] {
	var options runOptions
	for _, o := range opts {
		o(&options)
	}

	ch := make(chan Event, 16)
	it := &channelIterator{ch: ch, done: make(chan struct{})}

	go debug.CapturePanicReport(func() {

		defer close(it.done)
		defer close(ch)
		a.run(ctx, ch, dialogueID, message, options)

	})

	return it
}

func (a *Agent) run(
	ctx context.Context, ch chan<- Event,
	dialogueID string, userMessage string, opts runOptions,
) {
	// If a skill was pre-loaded via slash command, prepare its
	// content for system-message injection. Re-invocations via the
	// skill tool are harmless and re-deliver the body, so no
	// activation tracking is required.
	var preloadedSkillMsg string
	if opts.skillName != "" {
		if skill, ok := a.skillRegistry.Get(opts.skillName); ok {
			preloadedSkillMsg = skills.FormatSkillContent(skill)
		}
	}

	ctx = audit.WithAuditDialogueID(ctx, dialogueID)
	log := slog.With("struct", "agent.Agent", "dialogueID", dialogueID)

	maxOutput := a.config.MaxToolOutputBytes
	if maxOutput <= 0 {
		maxOutput = DefaultMaxToolOutputBytes
	}

	// Load or create dialogue
	var isNew bool
	dialogue, err := a.store.Get(ctx, dialogueID)
	if err != nil {
		if !errors.Is(err, storageapi.ErrNotFound) {
			emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf("get dialogue: %w", err)})
			return
		}
		isNew = true
		// New dialogue: seed with system prompt
		dialogue.ID = dialogueID
		dialogue.WorkspaceURI = a.config.Workspace.String()
		dialogue.Messages = []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: a.config.SystemPrompt},
		}
		log.Debug("new dialogue: added system prompt", "prompt", a.config.SystemPrompt)
	} else {
		log.Debug("found dialogue in storage: re-using",
			"messages", len(dialogue.Messages), "version", dialogue.Version,
			"ID", dialogue.ID, "updated", dialogue.UpdatedAt)
	}
	hasPersistedDialogue := !isNew

	// SessionStart context describes live session state, so it belongs in
	// the provider request but not in the durable user-authored turn.
	startRes := a.config.Hooks.Run(ctx, hooks.Payload{
		SessionID:     dialogueID,
		Cwd:           a.config.Workspace,
		HookEventName: hooks.EventSessionStart,
		Source:        sessionStartSource(isNew),
		Model:         a.config.Model.Name,
	})
	requestAdditionalContext := opts.additionalContext
	if startRes.AdditionalContext != "" {
		if requestAdditionalContext != "" {
			requestAdditionalContext = startRes.AdditionalContext + "\n\n" +
				requestAdditionalContext
		} else {
			requestAdditionalContext = startRes.AdditionalContext
		}
	}

	// Gather context resources in deterministic order. sync.Map.Range
	// iterates non-deterministically; sorting by URI string ensures the
	// system prompt prefix is stable across calls, which is critical for
	// prompt caching.
	type resourceEntry struct {
		uri     string
		content string
	}
	var resources []resourceEntry
	a.resources.Range(func(k, v any) bool {
		resources = append(resources, resourceEntry{
			uri:     fmt.Sprint(k),
			content: v.(string),
		})
		return true
	})
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].uri < resources[j].uri
	})
	resourceMsgs := make([]llmapi.Message, 0, len(resources))
	for _, r := range resources {
		resourceMsgs = append(resourceMsgs, llmapi.Message{
			Role: llmapi.RoleSystem,
			Content: fmt.Sprintf("The file with URI %s is "+
				"in the user's context:\n```\n%s\n```", r.uri, r.content),
		})
		log.Debug("added file to context", "uri", r.uri, "size", len(r.content))
	}

	// Content is the transcript representation. MultiContent is the model
	// representation when the two texts differ or content parts are present.
	displayMessage := userMessage
	if opts.displaySet {
		displayMessage = opts.displayMessage
	}
	userMsg := llmapi.Message{Role: llmapi.RoleUser, Content: displayMessage}
	if displayMessage != userMessage || len(opts.attachments) > 0 {
		// Providers that support content parts read MultiContent and
		// ignore Content, so the text has to be repeated as a part.
		userMsg.MultiContent = append(
			[]llmapi.ContentPart{{
				Type: llmapi.ContentPartTypeText,
				Text: userMessage,
			}},
			opts.attachments...)
	}

	// Build full message list
	messages := make([]llmapi.Message, 0,
		len(dialogue.Messages)+len(resourceMsgs)+1)
	messages = append(messages, dialogue.Messages...)
	messages = normalizeMessages(messages)
	messages = append(messages, resourceMsgs...)
	messages = append(messages, userMsg)

	// Track new messages for persistence.
	// For new dialogues, include the seeded system prompt so it is persisted.
	newMessages := make([]llmapi.Message, 0, len(dialogue.Messages)+1)
	if isNew {
		newMessages = append(newMessages, dialogue.Messages...)
	}
	newMessages = append(newMessages, userMsg)

	// Inject pre-computed tool call results so the agent starts with
	// these files already "read" — avoids redundant tool invocations.
	if len(opts.toolCallResults) > 0 {
		tcMsgs := buildToolCallMessages(opts.toolCallResults)
		messages = append(messages, tcMsgs...)
		newMessages = append(newMessages, tcMsgs...)
	}

	// defaultAutoCompactRatio is the fraction of the context window above
	// which the runtime auto-compacts without waiting for the model.
	const defaultAutoCompactRatio = 0.85

	autoCompactRatio := a.config.AutoCompactRatio
	if autoCompactRatio <= 0 {
		autoCompactRatio = defaultAutoCompactRatio
	}

	// Track usage across the entire Run invocation, plus the portion not yet
	// checkpointed to durable storage.
	var usage llmapi.DialogueUsage
	var pendingUsage llmapi.DialogueUsage
	runStart := time.Now()
	checkpointStart := runStart

	contextWindow := a.config.Model.ContextWindow

	// lastAPITokensSent caches the sent token count reported by the
	// provider in its most recent response. When available it is more
	// accurate than the local tiktoken estimate (especially for
	// non-OpenAI providers), so we prefer it for context-usage checks.
	// Falls back to CountTokens on the first iteration.
	var lastAPITokensSent int

	// response and reasoningResponse accumulate streamed text for the
	// fallback assistant message when doneData is absent. Declared outside
	// the loop so their internal buffers are reused across turns rather
	// than re-grown from zero on every iteration.
	var response strings.Builder
	var reasoningResponse strings.Builder

	// Recall relevant memories for this user message. The content is
	// injected into the user message at a fixed index inside the loop
	// (see below). Memory is recalled once per Run; "every turn" means
	// every user message / Run() call.
	recallStart := time.Now()
	memories, memErr := a.memory.Recall(ctx, a.resourceFiles(), displayMessage,
		a.config.Workspace.Path())
	recallDuration := time.Since(recallStart)
	if memErr != nil {
		log.Warn("memory recall failed", "error", memErr)
	} else {
		log.Debug("memory recall", "count", len(memories), "duration", recallDuration)
	}
	if len(memories) > 0 {
		emit(ctx, ch, Event{Type: EventMemoryRecall, Memories: memories, MemoryDuration: recallDuration})
	}
	memoryContent := FormatMemoryContent(memories)

	// userMsgIdx is the position of the user's message in `messages`.
	// Memory and project instructions are injected at this index every
	// iteration so the prefix content is stable for prompt caching.
	// The index accounts for any pre-computed tool results appended
	// after the user message, and is updated after compaction resets
	// `messages` to a shorter slice.
	userMsgIdx := len(messages) - 1
	if len(opts.toolCallResults) > 0 {
		userMsgIdx -= len(buildToolCallMessages(opts.toolCallResults))
	}

	// Snapshot tools once before the loop. Registry.Tools iterates
	// over a map, so calling it per-iteration would produce
	// non-deterministic ordering that busts the tools cache.
	tools := a.registry.Tools(a.provider())

	var transient []llmapi.Message
	var infos []toolCallInfo
	var toolMsgs []llmapi.Message
	var imageContentParts []llmapi.ContentPart
	// stopHookActive is set to true once the Stop hook has blocked
	// once and we appended a continuation message. The second
	// invocation passes this flag in the payload and ignores blocks.
	var stopHookActive bool
	// autoCompactedThisRun tracks whether we have already auto-compacted
	// during this Run() invocation. Compaction collapses the
	// conversation into a summary; if usage *still* exceeds the
	// threshold afterwards (realistic on small-ctx local models where
	// the system prompt + tool schemas alone push past 85%), the next
	// iteration would auto-compact again, then again, ad infinitum —
	// the user only ever sees a "compacting" spinner. One compact per
	// Run is the strongest guarantee that still lets users pin large
	// histories down between turns.
	autoCompactedThisRun := false
	persistPending := func() bool {
		if len(newMessages) == 0 {
			return false
		}
		pendingUsage.TotalDuration = time.Since(checkpointStart)
		if !a.persistMessages(ctx, ch, dialogueID, dialogue, newMessages, pendingUsage) {
			return false
		}
		if hasPersistedDialogue {
			dialogue.Messages = append(dialogue.Messages, newMessages...)
		} else {
			dialogue.Messages = append([]llmapi.Message(nil), newMessages...)
			hasPersistedDialogue = true
		}
		newMessages = nil
		pendingUsage = llmapi.DialogueUsage{}
		checkpointStart = time.Now()
		return true
	}
	for i := range a.config.MaxIterations {
		log.Debug("agent loop iteration",
			"iteration", i, "messages", len(messages))

		// Build request messages with transient injections.
		// These are not persisted — they reflect live state each turn.
		reqMessages := messages

		// Re-scan skill directories so out-of-band installations
		// (e.g. after request_skill) are picked up immediately.
		// The skills list is sorted by name, so content is stable
		// unless a skill is actually installed or removed.
		a.skillRegistry.Reload()

		// Clear element slots before truncating: llmapi.Message holds Content
		// strings (potentially large) plus ToolCalls slice, and a bare [:0]
		// reslice would keep them reachable until overwritten.
		clear(transient)
		transient = transient[:0]
		instructions := CommitAttributionInstructions(a.ModelEntry(), a.config.Attribution)
		if instructions != "" {
			transient = append(transient, llmapi.Message{Role: llmapi.RoleSystem, Content: instructions})
		}
		if section := skillsPromptSection(a.skillRegistry.List()); section != "" {
			transient = append(transient, llmapi.Message{Role: llmapi.RoleSystem, Content: section})
		}
		if preloadedSkillMsg != "" {
			transient = append(transient, llmapi.Message{Role: llmapi.RoleSystem, Content: preloadedSkillMsg})
		}
		if len(transient) > 0 {
			req := make([]llmapi.Message, 0, len(reqMessages)+len(transient))
			req = append(req, reqMessages[0])
			req = append(req, transient...)
			req = append(req, reqMessages[1:]...)
			reqMessages = req
		}

		// Inject live context into the user message. It is transient — NOT
		// persisted. userMsgIdx
		// tracks the user message position and is updated after
		// compaction; transient insertion shifts it uniformly.
		if a.config.ProjectInstructions != "" || memoryContent != "" ||
			requestAdditionalContext != "" {
			if len(reqMessages) == len(messages) {
				reqMessages = slices.Clone(reqMessages)
			}
			idx := userMsgIdx
			if len(transient) > 0 {
				idx += len(transient)
			}
			var contextParts []string
			if a.config.ProjectInstructions != "" {
				contextParts = append(contextParts, "<project-instructions>\n"+
					a.config.ProjectInstructions+"\n</project-instructions>")
			}
			if memoryContent != "" {
				contextParts = append(contextParts,
					"<memory-context>\n"+memoryContent+"\n</memory-context>")
			}
			if requestAdditionalContext != "" {
				contextParts = append(contextParts, requestAdditionalContext)
			}
			reqMessages[idx] = prependMessageText(reqMessages[idx],
				strings.Join(contextParts, "\n\n"))
		}

		// Inject transient hint when approaching the context window limit.
		// Prefer the provider-reported sent token count from the last
		// completion — it reflects the actual tokenizer. Fall back to
		// the local tiktoken estimate on the first iteration or when
		// the provider did not report usage.
		tokenCount := lastAPITokensSent
		if tokenCount == 0 {
			tokenCount, _ = a.svc.CountTokens(a.config.Model, reqMessages)
			tokenCount += estimateToolDefTokens(tools)
		}
		contextUsage := float64(tokenCount) / float64(contextWindow)
		log.Debug("context window usage",
			"tokens", tokenCount,
			"context_window", contextWindow,
			"usage_pct", int(contextUsage*100),
		)

		// Auto-compact: if usage exceeds the auto-compact ratio, compact
		// without waiting for the model to call the compact tool.
		//
		// Two guards prevent pathological behaviour observed with
		// small-context local models (e.g. 8192-ctx Qwen):
		//
		//  1. Skip when the conversation has no prior assistant turn.
		//     On the very first user message, the only thing
		//     compaction *could* do is replace "hello" with a summary
		//     of "hello" — the system prompt and tool schemas (which
		//     dominate token usage) are static and untouched by
		//     compaction. The user just sees a long "compacting"
		//     spinner and the model never gets to respond.
		//
		//  2. Skip after we already auto-compacted in this Run. If
		//     usage is still above threshold post-compact, compacting
		//     again will produce the same outcome — we already
		//     replaced the conversation with the smallest faithful
		//     summary we can. Looping wastes time and confuses the UX.
		//     The model will instead get the request and either
		//     respond or report a context-window-exceeded error.
		hasPriorAssistant := false
		for _, m := range messages {
			if m.Role == llmapi.RoleAssistant {
				hasPriorAssistant = true
				break
			}
		}
		shouldAutoCompact := contextWindow > 0 &&
			contextUsage >= autoCompactRatio &&
			hasPriorAssistant &&
			!autoCompactedThisRun
		if shouldAutoCompact {
			log.Info("auto-compacting: context usage above threshold",
				"usage_pct", int(contextUsage*100),
				"threshold_pct", int(autoCompactRatio*100),
			)
			// PreCompact (auto): a block here skips compaction for
			// this turn. We fall through and let the iteration
			// continue with the existing message set; the runtime
			// will call the LLM and may legitimately fail with a
			// context-too-large error, which is up to the operator.
			pcRes := a.config.Hooks.Run(ctx, hooks.Payload{
				SessionID:     dialogueID,
				Cwd:           a.config.Workspace,
				HookEventName: hooks.EventPreCompact,
				Trigger:       "auto",
			})
			if pcRes.Blocked() {
				log.Warn("auto-compact blocked by PreCompact hook", "reason", pcRes.Reason)
			} else {
				autoCompactedThisRun = true
				emit(ctx, ch, Event{Type: EventCompacting})
				dialogue.Messages = messages
				compactedDialogue, compactErr := a.compact(ctx, ch, dialogue)
				if compactErr != nil {
					log.Warn("auto-compact failed, continuing without compaction", "error", compactErr)
				} else {
					dialogue = compactedDialogue
					hasPersistedDialogue = true
					newMessages = nil
					messages = append(dialogue.Messages[:len(dialogue.Messages):len(dialogue.Messages)], resourceMsgs...)
					userMsgIdx = len(dialogue.Messages) - 1
					lastAPITokensSent = 0
					emit(ctx, ch, Event{Type: EventDone})
					continue
				}
			}
		}

		req := llmapi.Request{
			Messages:        reqMessages,
			PromptCacheKey:  dialogueID,
			Tools:           tools,
			ReasoningEffort: a.Effort(),
			MaxOutputTokens: a.MaxOutputTokens(),
			TokenCount:      lastAPITokensSent,
		}

		emit(ctx, ch, Event{Type: EventInferenceStart})
		inferenceStart := time.Now()
		log.Debug("creating completion", "messages", len(reqMessages), "tools", len(tools))
		// Layer 1 wire safety net: scrub every outgoing string of
		// invalid UTF-8 bytes so the proto-go marshaller cannot
		// reject the request.
		sanitizeRequest(&req)
		it, err := a.svc.CreateCompletion(ctx, a.config.Model, req)
		log.Debug("created completion", "error", err, "duration", time.Since(inferenceStart))
		if err != nil {
			emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf("create completion: %w", err)})
			usage.TotalDuration = time.Since(runStart)
			persistPending()
			return
		}
		emit(ctx, ch, Event{Type: EventInferenceReady})

		response.Reset()
		reasoningResponse.Reset()
		var doneData *llmapi.DoneData
		firstContent := false

		// Consume stream
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventTextDelta:
				if !firstContent {
					firstContent = true
					emit(ctx, ch, Event{Type: EventFirstContent})
				}
				emit(ctx, ch, Event{Type: EventText, Text: ev.Text})
				response.WriteString(ev.Text)
			case llmapi.EventReasoningDelta:
				if !firstContent {
					firstContent = true
					emit(ctx, ch, Event{Type: EventFirstContent})
				}
				emit(ctx, ch, Event{Type: EventReasoning, Reasoning: ev.Reasoning})
				reasoningResponse.WriteString(ev.Reasoning)
			case llmapi.EventToolCallDone:
				// Streaming hint — full tool calls come via doneData.
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			case llmapi.EventRateLimitWarning:
				emit(ctx, ch, Event{Type: EventRateLimitWarning, RateLimit: ev.RateLimit})
			case llmapi.EventStreamReset:
				response.Reset()
				reasoningResponse.Reset()
				doneData = nil
				firstContent = false
				emit(ctx, ch, Event{Type: EventDone})
				emit(ctx, ch, Event{Type: EventInferenceStart})
			case llmapi.EventStreamError:
				log.Debug("stream error during inference",
					"error", ev.Error,
					"messages_sent", len(reqMessages),
					"response_so_far_len", response.Len(),
					"reasoning_so_far_len", reasoningResponse.Len(),
					"elapsed", time.Since(inferenceStart),
				)
				emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf("stream: %w", ev.Error)})
				usage.TotalDuration = time.Since(runStart)
				persistPending()
				_ = it.Close()
				return
			}
		}
		inferenceDuration := time.Since(inferenceStart)

		err = it.Err()
		log.Debug("consumed completion response", "error", err, "duration", inferenceDuration)
		if err != nil {
			emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf("stream: %w", err)})
			usage.TotalDuration = time.Since(runStart)
			persistPending()
			return
		}
		_ = it.Close()

		// Build assistant message from doneData (source of truth).
		var assistantMsg llmapi.Message
		var finishReason llmapi.FinishReason
		var completionUsage llmapi.Usage
		if doneData != nil {
			assistantMsg = doneData.Message
			finishReason = doneData.FinishReason
			completionUsage = doneData.Usage
		} else {
			assistantMsg = llmapi.Message{
				Role:             llmapi.RoleAssistant,
				Content:          response.String(),
				ReasoningContent: reasoningResponse.String(),
			}
		}
		// Cache provider-reported sent tokens for the next iteration's
		// context-usage check. Reset to 0 after compaction so the next
		// iteration falls back to CountTokens with the new message set.
		lastAPITokensSent = completionUsage.TokensSent

		if assistantMessageHasReplayableContent(assistantMsg) {
			messages = append(messages, assistantMsg)
			newMessages = append(newMessages, assistantMsg)
		}

		// Emit a snapshot of cumulative usage after each completion so
		// the TUI can display token counts in the status hint.
		{
			snapshot := usage
			snapshot.Add(completionUsage, 0, inferenceDuration, 0)
			snapshot.TotalDuration = time.Since(runStart)
			emit(ctx, ch, Event{
				Type:  EventUsageUpdate,
				Usage: snapshot,
				Context: ContextSnapshot{
					TokensSent:     completionUsage.TokensSent,
					TokensReceived: completionUsage.TokensReceived,
					Window:         contextWindow,
					AutoCompactAt:  int(float64(contextWindow) * autoCompactRatio),
				},
			})
		}

		switch finishReason {
		case llmapi.FinishReasonLength:
			// Output truncated by token limit. Persist the partial
			// assistant message (including any reasoning-only content)
			// so the user can continue the conversation.
			usage.Add(completionUsage, 0, inferenceDuration, 0)
			usage.TotalDuration = time.Since(runStart)
			pendingUsage.Add(completionUsage, 0, inferenceDuration, 0)
			persistPending()
			emit(ctx, ch, Event{
				Type:         EventDone,
				FinishReason: finishReason,
				Context: ContextSnapshot{
					TokensSent:     completionUsage.TokensSent,
					TokensReceived: completionUsage.TokensReceived,
					Window:         contextWindow,
					AutoCompactAt:  int(float64(contextWindow) * autoCompactRatio),
				},
			})
			log.Debug("agent loop done: output truncated", "reason", finishReason)
			return

		case llmapi.FinishReasonStop:
			// Stop hook may block — i.e. instruct the agent to keep
			// going. We allow exactly one continuation per Run to
			// prevent infinite loops; the second invocation passes
			// stop_hook_active=true and ignores any further block.
			stopRes := a.config.Hooks.Run(ctx, hooks.Payload{
				SessionID:      dialogueID,
				Cwd:            a.config.Workspace,
				HookEventName:  hooks.EventStop,
				StopHookActive: stopHookActive,
			})
			if stopRes.Blocked() && !stopHookActive {
				stopHookActive = true
				reason := stopRes.Reason
				if reason == "" {
					reason = "Continue"
				}
				contMsg := llmapi.Message{Role: llmapi.RoleUser, Content: reason}
				messages = append(messages, contMsg)
				newMessages = append(newMessages, contMsg)
				usage.Add(completionUsage, 0, inferenceDuration, 0)
				pendingUsage.Add(completionUsage, 0, inferenceDuration, 0)
				emit(ctx, ch, Event{Type: EventDone, FinishReason: finishReason})
				continue
			}
			usage.Add(completionUsage, 0, inferenceDuration, 0)
			usage.TotalDuration = time.Since(runStart)
			pendingUsage.Add(completionUsage, 0, inferenceDuration, 0)
			persistPending()
			emit(ctx, ch, Event{
				Type:         EventDone,
				FinishReason: finishReason,
				Context: ContextSnapshot{
					TokensSent:     completionUsage.TokensSent,
					TokensReceived: completionUsage.TokensReceived,
					Window:         contextWindow,
					AutoCompactAt:  int(float64(contextWindow) * autoCompactRatio),
				},
			})
			log.Debug("agent loop done", "reason", finishReason)
			return

		case llmapi.FinishReasonPause:
			// Anthropic paused a long-running turn (pause_turn). The partial
			// assistant message was already appended above; re-enter the loop
			// without running tools or emitting EventDone so the next
			// CreateCompletion re-sends the conversation and the model resumes.
			usage.Add(completionUsage, 0, inferenceDuration, 0)
			pendingUsage.Add(completionUsage, 0, inferenceDuration, 0)
			persistPending()
			log.Debug("agent loop paused: resuming", "reason", finishReason)
			continue

		case llmapi.FinishReasonRefusal:
			// The model declined to continue for safety reasons. This is
			// terminal: run the Stop hook for parity but do not honor any
			// continuation, then surface a distinct refusal event so the TUI
			// can render a refusal banner rather than a generic error.
			a.config.Hooks.Run(ctx, hooks.Payload{
				SessionID:     dialogueID,
				Cwd:           a.config.Workspace,
				HookEventName: hooks.EventStop,
			})
			usage.Add(completionUsage, 0, inferenceDuration, 0)
			usage.TotalDuration = time.Since(runStart)
			pendingUsage.Add(completionUsage, 0, inferenceDuration, 0)
			persistPending()
			emit(ctx, ch, Event{
				Type:         EventRefusal,
				FinishReason: finishReason,
				Context: ContextSnapshot{
					TokensSent:     completionUsage.TokensSent,
					TokensReceived: completionUsage.TokensReceived,
					Window:         contextWindow,
					AutoCompactAt:  int(float64(contextWindow) * autoCompactRatio),
				},
			})
			log.Warn("agent loop done: model refused", "reason", finishReason)
			return

		case llmapi.FinishReasonToolCall:
			if len(assistantMsg.ToolCalls) == 0 {
				emit(ctx, ch, Event{Type: EventError,
					Error: errors.New("tool_calls finish reason but no tool calls in message")})
				usage.Add(completionUsage, 0, inferenceDuration, 0)
				usage.TotalDuration = time.Since(runStart)
				pendingUsage.Add(completionUsage, 0, inferenceDuration, 0)
				persistPending()
				log.Warn("agent loop done: no tool calls in response", "reason", finishReason)
				return
			}

			toolsStart := time.Now()
			emit(ctx, ch, Event{Type: EventToolsStart})

			// 1. Emit all EventToolCall events and resolve summaries upfront.
			// Clear element slots: toolCallInfo holds a tool.Interface and
			// argument strings that would otherwise stay reachable past [:0].
			clear(infos)
			infos = infos[:0]
			for _, call := range assistantMsg.ToolCalls {
				log.Debug("tool call",
					"name", call.Function.Name,
					"args", call.Function.Arguments,
				)
				tool, found := a.registry.Get(call.Function.Name, a.provider())
				var summary string
				if found {
					summary = tool.Summary(call.Function.Arguments)
				}
				infos = append(infos, toolCallInfo{call: call, tool: tool, found: found, summary: summary})
				emit(ctx, ch, Event{
					Type:          EventToolCall,
					ToolCallID:    call.ID,
					ToolName:      call.Function.Name,
					ToolArgs:      call.Function.Arguments,
					ToolSummary:   summary,
					ToolStartTime: time.Now(),
				})
			}

			// 2. Fan out: launch all tool executions in parallel,
			// unless a tool in the batch needs deterministic order, in
			// which case run them sequentially in call order so file
			// operations on the same path cannot race (RUNE-AGENT-98).
			serialize := false
			for _, info := range infos {
				if info.found && info.tool.NeedsDeterministicOrder() {
					serialize = true
					break
				}
			}

			results := make(chan executedToolCall, len(infos))
			var wg sync.WaitGroup
			for i, info := range infos {
				wg.Add(1)
				run := func() {
					func(i int, info toolCallInfo) {
						defer wg.Done()
						var result ToolResult
						var dur time.Duration
						if !info.found {
							log.Warn("unknown tool", "name", info.call.Function.Name)
							msg := fmt.Sprintf("error: unknown tool %q", info.call.Function.Name)
							if repl := a.registry.ReplacementFor(info.call.Function.Name, a.provider()); repl != "" {
								msg += fmt.Sprintf("; use the %q tool instead", repl)
							}
							result = ToolResult{
								Content: msg,
								IsError: true,
							}
						} else {
							toolCtx := WithCurrentModel(ctx, a.ModelEntry())
							toolCtx = WithParentToolCallID(toolCtx, info.call.ID)
							toolCtx = WithHooks(toolCtx, a.config.Hooks)
							toolCtx = WithWorkspaceURI(toolCtx, a.config.Workspace)
							toolCtx = WithDialogueID(toolCtx, dialogueID)
							toolCtx = WithPrompter(toolCtx, a.config.Prompter)
							toolStart := time.Now()
							result = info.tool.Execute(toolCtx, info.call.Function.Arguments)
							dur = time.Since(toolStart)
							log.Debug("executed tool",
								"name", info.call.Function.Name,
								"error", result.IsError,
								"output", len(result.Content),
								"duration", dur,
							)
						}
						capToolResult(&result, maxOutput)
						// Layer 1: belt-and-suspenders sanitisation so
						// a stray invalid UTF-8 byte in any tool
						// output (or in an error message that embeds
						// raw bytes) can never wedge the proto-go
						// marshaller downstream.
						result.Content = utf8validate.Sanitize(result.Content)
						results <- executedToolCall{index: i, info: info, result: result, duration: dur}
					}(i, info)
				}
				if serialize {
					run()
				} else {
					go debug.CapturePanicReport(run)
				}
			}
			go debug.CapturePanicReport(func() {
				wg.Wait()
				close(results)
			})

			// 3. Fan in: collect results in completion order, emit EventToolResult.
			toolMsgs = slices.Grow(toolMsgs[:0], len(infos))[:len(infos)]
			clear(toolMsgs)
			compacted := false
			// Image parts may carry large data URLs; clear before truncating.
			clear(imageContentParts)
			imageContentParts = imageContentParts[:0]
			var diagCandidates []executedToolCall
			for tr := range results {
				result := tr.result
				// Track successful apply_patch calls with touched files for auto-diagnostics.
				if !result.IsError && len(result.TouchedFiles) == 1 &&
					tr.info.call.Function.Name == "apply_patch" {
					diagCandidates = append(diagCandidates, tr)
				}

				// Handle compact: must be the sole tool call.
				if result.Compact {
					if len(assistantMsg.ToolCalls) > 1 {
						result = ToolResult{
							Content: "Compaction must be the only tool call in a response. " +
								"Call compact by itself without other tools.",
							IsError: true,
						}
					} else {
						// PreCompact (tool): a block here turns the
						// compact tool call into an error result.
						pcRes := a.config.Hooks.Run(ctx, hooks.Payload{
							SessionID:     dialogueID,
							Cwd:           a.config.Workspace,
							HookEventName: hooks.EventPreCompact,
							Trigger:       "tool",
						})
						if pcRes.Blocked() {
							result = ToolResult{
								Content: fmt.Sprintf("Compaction blocked by hook: %s", pcRes.Reason),
								IsError: true,
							}
						} else {
							emit(ctx, ch, Event{Type: EventCompacting})

							// Strip the trailing assistant message (the compact
							// tool call itself) — its tool result hasn't been
							// appended yet, and an unpaired tool_calls message
							// would cause an API error during summarization.
							compactD := dialogue
							compactD.Messages = messages[:len(messages)-1]
							compactedDialogue, compactErr := a.compact(ctx, ch, compactD)
							if compactErr != nil {
								result = ToolResult{
									Content: fmt.Sprintf("Compaction failed: %v. Continue without compacting.", compactErr),
									IsError: true,
								}
							} else {
								dialogue = compactedDialogue
								hasPersistedDialogue = true
								newMessages = nil
								messages = append(dialogue.Messages[:len(dialogue.Messages):len(dialogue.Messages)], resourceMsgs...)
								userMsgIdx = len(dialogue.Messages) - 1
								lastAPITokensSent = 0 // force re-count with compacted messages
								compacted = true
							}
						}
					}
				}

				// Handle clear context: reset to system prompt + tool result.
				if result.ClearContext {
					if len(assistantMsg.ToolCalls) > 1 {
						result = ToolResult{
							Content: "Clear context must be the only tool call in a response. " +
								"Call exit_plan_mode by itself without other tools.",
							IsError: true,
						}
					} else {
						clearedMsgs, clearErr := a.clearContext(ctx, ch, result.ApprovedPlan, dialogue)
						if clearErr != nil {
							result = ToolResult{
								Content: fmt.Sprintf("Clear context failed: %v. Continue without clearing.", clearErr),
								IsError: true,
							}
						} else {
							dialogue.Messages = clearedMsgs
							dialogue.ApprovedPlan = result.ApprovedPlan
							dialogue.Version = 1
							hasPersistedDialogue = true
							newMessages = nil
							messages = append(clearedMsgs[:len(clearedMsgs):len(clearedMsgs)], resourceMsgs...)
							userMsgIdx = len(clearedMsgs) - 1
							lastAPITokensSent = 0 // force re-count with cleared messages
							compacted = true      // reuse flag to skip normal message append
						}
					}
				}

				emit(ctx, ch, Event{
					Type:         EventToolResult,
					ToolCallID:   tr.info.call.ID,
					ToolName:     tr.info.call.Function.Name,
					ToolOutput:   result.Content,
					ToolSummary:  tr.info.summary,
					IsError:      result.IsError,
					ToolDuration: tr.duration,
				})
				// PostToolUse hook: may block tool output, replacing
				// the content the model sees with a structured reason.
				hres := a.config.Hooks.Run(ctx, hooks.Payload{
					SessionID:     dialogueID,
					Cwd:           a.config.Workspace,
					HookEventName: hooks.EventPostToolUse,
					ToolName:      tr.info.call.Function.Name,
					ToolInput:     toolInputJSON(tr.info.call.Function.Arguments),
					ToolResponse:  toolResponseJSON(result.Content, result.IsError),
					ToolUseID:     tr.info.call.ID,
				})
				if hres.Blocked() {
					result.Content = hres.Reason
					result.IsError = true
				}
				toolMsgs[tr.index] = llmapi.Message{
					Role:       llmapi.RoleTool,
					Content:    nonEmptyToolResult(result.Content),
					Name:       tr.info.call.Function.Name,
					ToolCallID: tr.info.call.ID,
				}
				if len(result.MultiContent) > 0 {
					imageContentParts = append(imageContentParts, result.MultiContent...)
				}
			}

			toolMsgs, assistantMsg = a.injectAutoDiagnostics(ctx, ch, messages, newMessages, assistantMsg, toolMsgs, diagCandidates, maxOutput, log)

			toolCallDuration := time.Since(toolsStart)
			usage.Add(completionUsage, len(infos), inferenceDuration, toolCallDuration)
			pendingUsage.Add(completionUsage, len(infos), inferenceDuration, toolCallDuration)
			log.Debug("executed all tools: continuing loop",
				"duration", toolCallDuration)
			if compacted {
				// Signal a turn boundary so the TUI closes the
				// current Turn and resets streaming state before
				// the next LLM iteration begins.
				emit(ctx, ch, Event{Type: EventDone})
				continue // skip normal flow, next iteration uses compacted messages
			}

			// 4. Append tool messages in original order.
			messages = append(messages, toolMsgs...)
			newMessages = append(newMessages, toolMsgs...)

			// 5. Inject a synthetic user message for image content.
			// Neither OpenAI nor Anthropic supports images in tool-role
			// messages, so we carry the image data in a user message.
			if len(imageContentParts) > 0 {
				imgMsg := llmapi.Message{
					Role:         llmapi.RoleUser,
					MultiContent: imageContentParts,
				}
				messages = append(messages, imgMsg)
				newMessages = append(newMessages, imgMsg)
			}

			persistPending()
			// Continue loop — next iteration feeds tool results to LLM

		default:
			// Unexpected finish reason, persist and finish
			emit(ctx, ch, Event{Type: EventError,
				Error: fmt.Errorf("unexpected finish reason: %s", finishReason)})
			usage.Add(completionUsage, 0, inferenceDuration, 0)
			usage.TotalDuration = time.Since(runStart)
			pendingUsage.Add(completionUsage, 0, inferenceDuration, 0)
			persistPending()
			log.Warn("agent loop done: unexpected finish reason",
				"reason", finishReason,
				"text_len", len(assistantMsg.Content),
				"reasoning_len", len(assistantMsg.ReasoningContent),
				"tool_calls", len(assistantMsg.ToolCalls),
				"tokens_sent", completionUsage.TokensSent,
				"tokens_received", completionUsage.TokensReceived)
			return
		}
	}

	// Max iterations reached
	emit(ctx, ch, Event{Type: EventError,
		Error: fmt.Errorf("max iterations (%d) reached", a.config.MaxIterations)})
	usage.TotalDuration = time.Since(runStart)
	persistPending()
	log.Warn("agent loop done: reached max iterations", "max", a.config.MaxIterations)
}

// normalizeMessages sanitizes a message slice loaded from storage so
// it forms a valid conversation for the LLM. It fixes two classes of
// problems that arise when a previous turn was interrupted:
//
//  1. Reasoning-only assistant messages: if the model was truncated
//     before producing any text, the message has ReasoningContent but
//     empty Content. APIs reject empty text content blocks, so the
//     reasoning is moved into Content.
//
//  2. Orphaned tool calls / results: an assistant message may contain
//     tool calls whose results were never persisted (e.g. the turn was
//     interrupted before tool execution). Conversely, tool result
//     messages may lack a preceding tool call. Anthropic also requires
//     tool results to appear in the immediately-following user turn, so
//     a matching result ID later in the transcript is still invalid.
//     Orphaned or non-adjacent tool calls are stripped from the assistant
//     message and orphaned or non-adjacent tool results are removed
//     entirely.
func normalizeMessages(messages []llmapi.Message) []llmapi.Message {
	// Mark only structurally valid tool-call pairs. A tool result is valid
	// only when it appears in the consecutive RoleTool message group that
	// immediately follows the assistant message containing its tool call.
	validToolCallIDs := make(map[int]map[string]struct{})
	validToolResultIndexes := make(map[int]struct{})
	// resultNameByIndex backfills a tool result's Name from its matching tool
	// call when the result omitted it. Gemini requires function_response.name,
	// and legacy sessions (or any missed injection site) may lack it.
	resultNameByIndex := make(map[int]string)
	for i := range messages {
		if messages[i].Role != llmapi.RoleAssistant || len(messages[i].ToolCalls) == 0 {
			continue
		}

		callIDs := make(map[string]struct{}, len(messages[i].ToolCalls))
		callNames := make(map[string]string, len(messages[i].ToolCalls))
		for _, tc := range messages[i].ToolCalls {
			callIDs[tc.ID] = struct{}{}
			callNames[tc.ID] = tc.Function.Name
		}

		seenResults := make(map[string]struct{}, len(messages[i].ToolCalls))
		for j := i + 1; j < len(messages) && messages[j].Role == llmapi.RoleTool; j++ {
			id := messages[j].ToolCallID
			if id == "" {
				continue
			}
			if _, ok := callIDs[id]; !ok {
				continue
			}
			if _, ok := seenResults[id]; ok {
				continue
			}

			seenResults[id] = struct{}{}
			validToolResultIndexes[j] = struct{}{}
			if messages[j].Name == "" {
				resultNameByIndex[j] = callNames[id]
			}
			if validToolCallIDs[i] == nil {
				validToolCallIDs[i] = make(map[string]struct{}, len(messages[i].ToolCalls))
			}
			validToolCallIDs[i][id] = struct{}{}
		}
	}

	n := 0
	for i := range messages {
		msg := &messages[i]

		// Strip orphaned or non-adjacent tool calls from assistant messages.
		if len(msg.ToolCalls) > 0 {
			kept := msg.ToolCalls[:0]
			for _, tc := range msg.ToolCalls {
				if _, ok := validToolCallIDs[i][tc.ID]; ok {
					kept = append(kept, tc)
				}
			}
			// Zero any slots beyond the new length so dropped ToolCall ID and
			// Arguments strings can be GC'd before the slice header is reused.
			clear(msg.ToolCalls[len(kept):])
			msg.ToolCalls = kept
		}

		// Drop orphaned or non-adjacent tool results.
		if msg.Role == llmapi.RoleTool {
			if _, ok := validToolResultIndexes[i]; !ok {
				continue
			}
			if name := resultNameByIndex[i]; name != "" {
				msg.Name = name
			}
		}

		if msg.Role == llmapi.RoleAssistant && !assistantMessageHasReplayableContent(*msg) {
			continue
		}

		messages[n] = *msg
		n++
	}
	return messages[:n]
}

func assistantMessageHasReplayableContent(msg llmapi.Message) bool {
	return msg.Content != "" ||
		len(msg.ToolCalls) > 0 ||
		len(msg.MultiContent) > 0 ||
		slices.ContainsFunc(msg.ReasoningBlocks, func(rb llmapi.ReasoningBlock) bool {
			return rb.Signature != "" || rb.Data != ""
		})
}

func (a *Agent) injectAutoDiagnostics(
	ctx context.Context,
	ch chan<- Event,
	messages []llmapi.Message,
	newMessages []llmapi.Message,
	assistantMsg llmapi.Message,
	toolMsgs []llmapi.Message,
	diagCandidates []executedToolCall,
	maxOutput int,
	log *slog.Logger,
) ([]llmapi.Message, llmapi.Message) {
	if len(diagCandidates) == 0 {
		return toolMsgs, assistantMsg
	}

	diagTool, ok := a.registry.Get("check_file_errors", a.provider())
	if !ok {
		return toolMsgs, assistantMsg
	}

	assistantIdx := len(messages) - 1
	for _, cand := range diagCandidates {
		filePath := cand.result.TouchedFiles[0]

		// Resolve the language id so the disable set can be keyed by it.
		// If the language cannot be determined we skip auto-injection
		// (the LSP would fail anyway) without polluting the disable set.
		langID, err := languages.LanguageForFile(filepath.Base(filePath))
		if err != nil {
			log.Debug("auto-diagnostics: skipping, unknown language",
				"file", filePath, "error", err)
			continue
		}
		if _, disabled := a.autoDiagDisabled.Load(langID); disabled {
			log.Debug("auto-diagnostics: skipping, language disabled this session",
				"file", filePath, "language", langID)
			continue
		}

		syntheticID := "auto-diag-" + cand.info.call.ID
		diagArgs := fmt.Sprintf(`{"path":%q}`, filePath)
		diagSummary := diagTool.Summary(diagArgs)

		syntheticCall := llmapi.ToolCall{
			ID:   syntheticID,
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionCall{
				Name:      "check_file_errors",
				Arguments: diagArgs,
			},
		}
		assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, syntheticCall)

		// When the assistant message carries verbatim provider items
		// (Responses API replay), the converter prefers ProviderItems
		// over ToolCalls (see openai.responsesInputFromMessages). We
		// must also append a synthetic function_call provider item so
		// that the matching function_call_output we add below has its
		// referenced call present in the next request's input array.
		// Without this, the Codex/Responses backend rejects the
		// follow-up request with "No tool call found for function call
		// output with call_id auto-diag-...".
		if len(assistantMsg.ProviderItems) > 0 {
			syntheticItem, err := json.Marshal(map[string]any{
				"type":      "function_call",
				"call_id":   syntheticID,
				"name":      "check_file_errors",
				"arguments": diagArgs,
			})
			if err == nil {
				assistantMsg.ProviderItems = append(
					assistantMsg.ProviderItems, syntheticItem)
			} else {
				log.Warn("auto-diagnostics: failed to marshal synthetic provider item",
					"error", err)
			}
		}

		emit(ctx, ch, Event{
			Type:          EventToolCall,
			ToolCallID:    syntheticID,
			ToolName:      "check_file_errors",
			ToolArgs:      diagArgs,
			ToolSummary:   diagSummary,
			ToolStartTime: time.Now(),
		})

		diagStart := time.Now()
		diagResult := diagTool.Execute(
			WithParentToolCallID(WithCurrentModel(ctx, a.ModelEntry()), syntheticID),
			diagArgs,
		)
		diagDur := time.Since(diagStart)
		if !diagResult.IsError {
			diagResult.Content = TruncateMiddle(diagResult.Content, maxOutput)
		}

		log.Debug("auto-diagnostics",
			"file", filePath,
			"error", diagResult.IsError,
			"output", len(diagResult.Content),
			"duration", diagDur,
		)

		if diagResult.IsError && isNoLSPForLanguage(diagResult.Content, langID) {
			a.autoDiagDisabled.Store(langID, struct{}{})
			log.Debug("auto-diagnostics: disabling for language this session",
				"language", langID, "file", filePath)
		}

		emit(ctx, ch, Event{
			Type:         EventToolResult,
			ToolCallID:   syntheticID,
			ToolName:     "check_file_errors",
			ToolOutput:   diagResult.Content,
			ToolSummary:  diagSummary,
			IsError:      diagResult.IsError,
			ToolDuration: diagDur,
		})

		toolMsgs = append(toolMsgs, llmapi.Message{
			Role:       llmapi.RoleTool,
			Content:    diagResult.Content,
			Name:       "check_file_errors",
			ToolCallID: syntheticID,
		})
	}

	messages[assistantIdx] = assistantMsg
	newMessages[len(newMessages)-1] = assistantMsg
	return toolMsgs, assistantMsg
}

// isNoLSPForLanguage reports whether a check_file_errors error message
// indicates that no language server is available for the file's language,
// as opposed to a transient or generic diagnostic failure. The matched
// phrases are the stable strings produced by the LSP manager that survive
// the gRPC boundary (the in-process ErrNoServer sentinel does not).
func isNoLSPForLanguage(content, langID string) bool {
	return strings.Contains(content, "LSP is not supported yet") ||
		strings.Contains(content, "no language server") ||
		strings.Contains(content, "server "+langID+" not running")
}

func (a *Agent) persistMessages(
	ctx context.Context, ch chan<- Event, dialogueID string,
	dialogue dialoguemanager.Dialogue, newMessages []llmapi.Message,
	usage llmapi.DialogueUsage,
) bool {
	if len(newMessages) == 0 {
		return false
	}
	// If the caller's context is already cancelled (e.g. tab closed or
	// request cancelled), use a background context with a timeout so
	// the storage operation can still complete and the conversation
	// state is not lost.
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		slog.Debug("agent: persisting messages with background context", "dialogueID", dialogueID, "messages", len(newMessages))
	}
	err := a.store.Create(ctx, dialoguemanager.Dialogue{
		ID:           dialogueID,
		AgentID:      a.config.AgentID,
		Model:        a.config.Model.Name,
		WorkspaceURI: a.config.Workspace.String(),
		SubAgent:     a.config.SubAgent,
		Messages:     newMessages,
		Usage:        usage,
	})
	if errors.Is(err, storageapi.ErrAlreadyExists) {
		err = a.store.AppendMessages(ctx, dialogue, newMessages, usage)
	}
	if err != nil {
		slog.Error("agent: persist messages", "error", err, "dialogueID", dialogueID)
		emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf(
			"persist messages: %w", err)})
		return false
	}
	return true
}

// compact summarizes the conversation, persists the compacted messages,
// and returns them. On failure it emits EventError and returns the
// error so the caller can fall through.
func (a *Agent) compact(
	ctx context.Context, ch chan<- Event,
	d dialoguemanager.Dialogue,
) (dialoguemanager.Dialogue, error) {
	summarizeSvc := a.svc
	if a.config.CompactSvc != nil {
		summarizeSvc = a.config.CompactSvc
	}

	compactedMsgs, archivedID, err := CompactDialogue(ctx, summarizeSvc, a.config.Model, a.store, d)
	if err != nil {
		emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf("compact: %v", err)})
		return dialoguemanager.Dialogue{}, err
	}

	emit(ctx, ch, Event{Type: EventCompacted, ArchivedDialogueID: archivedID})
	d.Messages = compactedMsgs
	d.Version = 1
	return d, nil
}

// extractSkillContent finds tool results containing activated skill content
// and converts them to system messages for re-injection after compaction.
func extractSkillContent(messages []llmapi.Message) []llmapi.Message {
	var result []llmapi.Message
	seen := make(map[string]bool)
	for _, m := range messages {
		if m.Role != llmapi.RoleTool || !strings.Contains(m.Content, "<skill_content ") {
			continue
		}
		if seen[m.Content] {
			continue
		}
		seen[m.Content] = true
		result = append(result, llmapi.Message{
			Role:    llmapi.RoleSystem,
			Content: m.Content,
		})
	}
	return result
}

func approvedPlanMessages(plan *dialoguemanager.ApprovedPlan) []llmapi.Message {
	if plan == nil {
		return nil
	}
	return []llmapi.Message{{
		// Anchor the conversation with a user turn so subsequent
		// assistant tool_use turns are valid for providers that require
		// strict user/assistant alternation (e.g. Anthropic). System
		// messages are extracted out of the messages array by those
		// providers, so an assistant tool_use directly after the system
		// prompt would produce
		//
		//   "messages.0: tool_use ids were found without tool_result
		//   blocks immediately after"
		//
		// when the conversation resumes from compaction or after a
		// cleared exit_plan_mode turn.
		Role:    llmapi.RoleUser,
		Content: fmt.Sprintf("Plan approved. Saved to %s\n\n%s", plan.Path, plan.Body),
	}}
}

// clearContext replaces the conversation with just a system prompt and
// the approved-plan as a user-anchor message, archives the old messages,
// and overwrites the current dialogue in-place. The dialogue ID never
// changes. The plan is also persisted as out-of-band metadata so it
// survives further compactions.
func (a *Agent) clearContext(
	ctx context.Context, ch chan<- Event,
	approvedPlan *dialoguemanager.ApprovedPlan,
	d dialoguemanager.Dialogue,
) ([]llmapi.Message, error) {
	archivedID, err := NextArchivedID(ctx, a.store, d.ID)
	if err != nil {
		emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf("clear context: archive: %v", err)})
		return nil, err
	}

	clearedMsgs := append(
		[]llmapi.Message{{Role: llmapi.RoleSystem, Content: a.config.SystemPrompt}},
		approvedPlanMessages(approvedPlan)...,
	)

	if err := a.store.ArchiveAndReplace(ctx, dialoguemanager.ArchiveAndReplaceParams{
		Dialogue:           d,
		ArchivedDialogueID: archivedID,
		Messages:           clearedMsgs,
		ApprovedPlan:       approvedPlan,
	}); err != nil {
		emit(ctx, ch, Event{Type: EventError, Error: fmt.Errorf("clear context: %v", err)})
		return nil, err
	}

	emit(ctx, ch, Event{Type: EventCompacted, ArchivedDialogueID: archivedID})
	return clearedMsgs, nil
}

// CompactSummaryPrefix is prepended to the LLM-generated summary when
// building the compacted user message for conversations without an approved
// plan. It explicitly tells the model to resume execution from the summary
// instead of drifting into re-planning. Placing the summary in a user message
// (rather than assistant) avoids assistant prefill, which some providers reject.
const CompactSummaryPrefix = "Resume from the compacted context below and continue the user's current task immediately. " +
	"Do NOT restart the task, create a new plan, or ask for approval unless the user explicitly requests that. " +
	"The summary below captures the earlier portion of the conversation and the current state of work.\n\n"

// CompactResumePrefix is used when a conversation already contains an approved
// plan. In that case we preserve the plan separately and compact only the
// current progress/state into a resume message that tells the model to keep
// implementing rather than re-plan.
const CompactResumePrefix = "Continue executing the approved plan immediately. " +
	"Do NOT create a new plan, ask for plan approval, or re-plan work that is already specified. " +
	"Keep implementing.\n\nCurrent state and progress from before compaction:\n\n"

// CompactResumeSuffix appends an explicit encouragement to continue execution.
const CompactResumeSuffix = "\n\nKeep implementing from this state."

// ArchivedID returns the base archive dialogue ID for the given dialogue.
// It strips any existing "-archived" (with optional numeric suffix) to avoid accumulation.
func ArchivedID(dialogueID string) string {
	dialogueID = strings.TrimSuffix(dialogueID, "-archived")
	dialogueID = strings.TrimSuffix(dialogueID, "-compacted")
	dialogueID = strings.TrimSuffix(dialogueID, "-cleared")
	return dialogueID + "-archived"
}

// NextArchivedID finds the next available archive ID by probing the store.
// It returns "<base>-archived" if that slot is free, otherwise
// "<base>-archived-2", "<base>-archived-3", etc.
func NextArchivedID(ctx context.Context, store dialoguemanager.Store, dialogueID string) (string, error) {
	base := ArchivedID(dialogueID)
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

var (
	reAnalysis   = regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
	reSummary    = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	reBlankLines = regexp.MustCompile(`\n\n+`)
)

// SummarizePrompt is the prompt sent to the LLM when compacting a
// conversation. It instructs the model to produce a structured 9-section
// summary wrapped in <summary> tags, with an optional <analysis>
// scratchpad that is stripped before use.
const SummarizePrompt = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:
1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like: file names, full code snippets, function signatures, file edits
   - Errors that you ran into and how you fixed them
   - Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents in detail
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Pay special attention to the most recent messages and include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List all errors that you ran into, and how you fixed them. Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results. These are critical for understanding the users' feedback and changing intent.
7. Pending Tasks: Outline any pending tasks that you have explicitly been asked to work on.
8. Current Work: Describe in detail precisely what was being worked on immediately before this summary request, paying special attention to the most recent messages from both user and assistant. Include file names and code snippets where applicable.
9. Optional Next Step: List the next step that you will take that is related to the most recent work you were doing. IMPORTANT: ensure that this step is DIRECTLY in line with the user's most recent explicit requests, and the task you were working on immediately before this summary request. If your last task was concluded, then only list next steps if they are explicitly in line with the users request. Do not start on tangential requests or really old requests that were already completed without confirming with the user first.
                       If there is a next step, include direct quotes from the most recent conversation showing exactly what task you were working on and where you left off. This should be verbatim to ensure there's no drift in task interpretation.

Please provide your output in the following format:

<analysis>
[Your thought process, checking each required element]
</analysis>

<summary>
[Your summary here, with all 9 sections]
</summary>

IMPORTANT: Do NOT use any tools. You MUST respond with ONLY the <summary>...</summary> block as your text output.`

// cleanSummary strips the <analysis> scratchpad from the raw LLM output
// and extracts the <summary> content. If no <summary> tags are found the
// text is returned as-is (plain-text fallback).
func cleanSummary(raw string) string {
	text := reAnalysis.ReplaceAllString(raw, "")
	if m := reSummary.FindStringSubmatch(text); len(m) >= 2 {
		inner := strings.TrimSpace(m[1])
		text = reSummary.ReplaceAllLiteralString(text, "Summary:\n"+inner)
	}
	text = reBlankLines.ReplaceAllString(text, "\n")
	return strings.TrimSpace(text)
}

// Summarize sends the given messages to the LLM and asks it to produce
// a structured summary. It returns the cleaned summary text.
func Summarize(ctx context.Context, svc llmapi.Service, model llmapi.ModelEntry, messages []llmapi.Message) (string, error) {
	messages = normalizeMessages(slices.Clone(messages))

	prompt := llmapi.Message{
		Role:    llmapi.RoleUser,
		Content: SummarizePrompt,
	}
	summaryReq := llmapi.Request{
		Messages: append(messages, prompt),
	}

	sanitizeRequest(&summaryReq)
	it, err := svc.CreateCompletion(ctx, model, summaryReq)
	if err != nil {
		return "", err
	}
	defer it.Close() //nolint:errcheck

	var sb strings.Builder
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		if ev.Type == llmapi.EventTextDelta {
			sb.WriteString(ev.Text)
		}
		if ev.Type == llmapi.EventStreamError {
			return "", ev.Error
		}
	}
	if err := it.Err(); err != nil {
		return "", err
	}
	summary := cleanSummary(sb.String())
	if summary == "" {
		return "", fmt.Errorf("LLM returned an empty summary")
	}
	return summary, nil
}

// CompactDialogue summarizes the conversation, archives the old messages
// under a unique archived ID, and overwrites the current dialogue in-place
// with the compacted messages. The dialogue ID never changes. It returns
// both the compacted messages and the archived dialogue ID.
//
// Manual compact callers (e.g. /compact slash command) may pass
// WithCompactHooks to fire the PreCompact hook with trigger="manual".
// A blocked hook is returned as a regular error.
func CompactDialogue(
	ctx context.Context,
	svc llmapi.Service,
	model llmapi.ModelEntry,
	store dialoguemanager.Store,
	d dialoguemanager.Dialogue,
	opts ...CompactOption,
) (compactedMsgs []llmapi.Message, archivedDialogueID string, err error) {
	var copts compactOptions
	for _, o := range opts {
		o(&copts)
	}

	cwd, _ := d.Workspace()
	pcRes := copts.hooks.Run(ctx, hooks.Payload{
		SessionID:     d.ID,
		Cwd:           cwd,
		HookEventName: hooks.EventPreCompact,
		Trigger:       "manual",
	})
	if pcRes.Blocked() {
		reason := pcRes.Reason
		if reason == "" {
			reason = "compaction blocked by PreCompact hook"
		}
		return nil, "", errors.New(reason)
	}

	summaryText, err := Summarize(ctx, svc, model, d.Messages)
	if err != nil {
		return nil, "", err
	}

	archivedID, err := NextArchivedID(ctx, store, d.ID)
	if err != nil {
		return nil, "", fmt.Errorf("archive: %w", err)
	}

	// Re-inject the system prompt from the original messages.
	var systemPrompt string
	if len(d.Messages) > 0 && d.Messages[0].Role == llmapi.RoleSystem {
		systemPrompt = d.Messages[0].Content
	}

	compactedMsgs = []llmapi.Message{{Role: llmapi.RoleSystem, Content: systemPrompt}}

	// Preserve approved-plan metadata and activated skills from the old dialogue.
	var preserved []llmapi.Message
	preserved = append(preserved, approvedPlanMessages(d.ApprovedPlan)...)
	preserved = append(preserved, extractSkillContent(d.Messages)...)
	if len(preserved) > 0 {
		compactedMsgs = append(compactedMsgs, preserved...)
	}

	if d.ApprovedPlan != nil {
		compactedMsgs = append(compactedMsgs, llmapi.Message{
			Role:    llmapi.RoleUser,
			Content: CompactResumePrefix + summaryText + CompactResumeSuffix,
		})
	} else {
		compactedMsgs = append(compactedMsgs, llmapi.Message{
			Role:    llmapi.RoleUser,
			Content: CompactSummaryPrefix + summaryText,
		})
	}

	if err := store.ArchiveAndReplace(ctx, dialoguemanager.ArchiveAndReplaceParams{
		Dialogue:           d,
		ArchivedDialogueID: archivedID,
		Messages:           compactedMsgs,
		ApprovedPlan:       d.ApprovedPlan,
	}); err != nil {
		return nil, "", fmt.Errorf("persist: %w", err)
	}

	return compactedMsgs, archivedID, nil
}

func emit(ctx context.Context, ch chan<- Event, ev Event) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// sessionStartSource returns the SessionStart hook source value
// ("startup" for newly-created dialogues, "resume" for existing ones).
func sessionStartSource(isNew bool) string {
	if isNew {
		return "startup"
	}
	return "resume"
}

// CompactOption configures CompactDialogue.
type CompactOption func(*compactOptions)

type compactOptions struct {
	hooks *hooks.Runner
}

// WithCompactHooks fires the PreCompact hook (manual trigger) before
// summarizing. A blocked hook turns into a returned error.
func WithCompactHooks(r *hooks.Runner) CompactOption {
	return func(o *compactOptions) { o.hooks = r }
}

// toolInputJSON returns the tool's raw arguments string as a
// json.RawMessage when it parses as JSON, otherwise it wraps the
// arguments in a JSON string.
func toolInputJSON(args string) []byte {
	if args == "" {
		return nil
	}
	var probe any
	if err := json.Unmarshal([]byte(args), &probe); err == nil {
		return []byte(args)
	}
	b, _ := json.Marshal(args)
	return b
}

// toolResponseJSON encodes the tool result content + error flag as a
// JSON object suitable for the PostToolUse payload.
func toolResponseJSON(content string, isErr bool) []byte {
	b, _ := json.Marshal(map[string]any{
		"content":  content,
		"is_error": isErr,
	})
	return b
}

// channelIterator adapts a channel to iterator.Iterator[Event].
type channelIterator struct {
	ch   <-chan Event
	done chan struct{}
	cur  Event
	err  error
}

func (c *channelIterator) Next(ctx context.Context) (Event, bool) {
	select {
	case ev, ok := <-c.ch:
		if !ok {
			return Event{}, false
		}
		c.cur = ev
		if ev.Type == EventError {
			c.err = ev.Error
		}
		return ev, true
	case <-ctx.Done():
		c.err = ctx.Err()
		return Event{}, false
	}
}

func (c *channelIterator) Err() error {
	return c.err
}

// Close drains any pending events and blocks until the producer goroutine
// has finished. This provides a natural join point so callers that use
// `defer it.Close()` wait for all background work (including post-cancel
// state persistence) before proceeding.
func (c *channelIterator) Close() error {
	for {
		select {
		case _, ok := <-c.ch:
			if !ok {
				<-c.done
				return nil
			}
		case <-c.done:
			// Drain any remaining buffered events so the producer can
			// finish closing without blocking on a full channel.
			for range c.ch { //nolint:revive
			}
			return nil
		}
	}
}

// sanitizeRequest scrubs every string field in req of invalid UTF-8
// bytes, replacing each with U+FFFD. Call this immediately before
// passing the request to llmapi.Service.CreateCompletion so the
// proto-go marshaller cannot reject the request for any single byte
// that slipped past upstream sanitisation.
func sanitizeRequest(req *llmapi.Request) {
	if req == nil {
		return
	}
	for i := range req.Messages {
		m := &req.Messages[i]
		m.Content = utf8validate.Sanitize(m.Content)
		m.ReasoningContent = utf8validate.Sanitize(m.ReasoningContent)
		m.ToolCallID = utf8validate.Sanitize(m.ToolCallID)
		m.Name = utf8validate.Sanitize(m.Name)
		for j := range m.MultiContent {
			cp := &m.MultiContent[j]
			cp.Text = utf8validate.Sanitize(cp.Text)
			cp.ImageURL = utf8validate.Sanitize(cp.ImageURL)
		}
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			tc.ID = utf8validate.Sanitize(tc.ID)
			tc.Function.Name = utf8validate.Sanitize(tc.Function.Name)
			tc.Function.Arguments = utf8validate.Sanitize(tc.Function.Arguments)
		}
	}
	req.PromptCacheKey = utf8validate.Sanitize(req.PromptCacheKey)
	for i := range req.Tools {
		t := &req.Tools[i]
		t.Function.Name = utf8validate.Sanitize(t.Function.Name)
		t.Function.Description = utf8validate.Sanitize(t.Function.Description)
	}
}

// buildToolCallMessages converts pre-computed ToolCallResults into the
// assistant + tool message pairs that would have been produced by
// actual tool executions. The assistant message contains all tool
// calls; each result follows as a separate tool-role message.
func buildToolCallMessages(results []ToolCallResult) []llmapi.Message {
	calls := make([]llmapi.ToolCall, len(results))
	for i, r := range results {
		calls[i] = llmapi.ToolCall{
			ID:   fmt.Sprintf("precall-%d", i),
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionCall{
				Name:      r.ToolName,
				Arguments: utf8validate.Sanitize(r.Arguments),
			},
		}
	}

	msgs := make([]llmapi.Message, 0, 1+len(results))
	msgs = append(msgs, llmapi.Message{
		Role:      llmapi.RoleAssistant,
		ToolCalls: calls,
	})
	for i, r := range results {
		msgs = append(msgs, llmapi.Message{
			Role:       llmapi.RoleTool,
			Content:    nonEmptyToolResult(utf8validate.Sanitize(r.Content)),
			Name:       calls[i].Function.Name,
			ToolCallID: calls[i].ID,
		})
	}
	return msgs
}

// estimateToolDefTokens approximates the token overhead of tool
// definitions without JSON serialization. It walks each tool's name,
// description, and parameter schema to estimate the JSON byte length,
// then divides by 4 (a conservative chars-per-token ratio for
// structured text). Zero allocations.
func estimateToolDefTokens(tools []llmapi.Tool) int {
	var n int
	for i := range tools {
		f := &tools[i].Function
		// JSON envelope: {"type":"...","function":{"name":"...","description":"...","parameters":...}}
		n += len(tools[i].Type) + len(f.Name) + len(f.Description) + 60
		if f.Parameters != nil {
			n += estimateJSONSize(f.Parameters)
		}
	}
	return n / 4
}

// estimateJSONSize approximates the JSON byte length of a value
// without serializing it. Handles the types that encoding/json
// produces when unmarshalling into any (map, slice, string, float64, bool, nil).
func estimateJSONSize(v any) int {
	switch v := v.(type) {
	case map[string]any:
		n := 2 // {}
		for k, val := range v {
			n += len(k) + 4 + estimateJSONSize(val) // "key":val,
		}
		return n
	case []any:
		n := 2 // []
		for _, val := range v {
			n += estimateJSONSize(val) + 1 // val,
		}
		return n
	case string:
		return len(v) + 2
	case bool:
		return 5
	case float64:
		return 10
	default:
		return 4 // null
	}
}
