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
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

// ToolResult is the result of executing a tool.
type ToolResult struct {
	Content      string
	MultiContent []llmapi.ContentPart // Image/multi-modal content for user-message injection.
	IsError      bool
	Compact      bool // Signals that the agent should compact the conversation.
	// CompactInstructions is optional guidance for the compaction summary.
	// It is only read when Compact is set.
	CompactInstructions string
	ClearContext        bool // Signals that the agent should clear the conversation context.
	ApprovedPlan        *dialoguemanager.ApprovedPlan
	DropToolResultIDs   []string         // Tool call IDs whose results should be truncated in history.
	TouchedFiles        []string         // Files that were created or modified (not deleted) by this tool.
	ReadObservation     *ReadObservation // Optional successful text-read coverage; not persisted.
}

// ReadObservation identifies the full file version and the zero-based,
// half-open line range actually returned. Images and failed reads omit it.
type ReadObservation struct {
	Path        string
	ContentHash [32]byte
	StartLine   int
	EndLine     int
}

// Tool is a capability that the agent can invoke via the LLM.
type Tool interface {
	Definition() llmapi.Tool
	Execute(ctx context.Context, arguments string) ToolResult
	// Summary returns a short human-readable description of the
	// arguments for display in the TUI. On parse error it returns
	// an empty string and the TUI falls back to formatToolArgs.
	Summary(arguments string) string
	// NeedsDeterministicOrder reports whether this tool must run
	// sequentially relative to the other tool calls in the same
	// assistant turn. Tools whose Execute mutates the filesystem or
	// spawns processes return true: when any tool in a batch needs
	// deterministic order, the agent loop runs the whole batch in
	// call order instead of fanning out in parallel, so file
	// operations on the same path cannot race (e.g. bash "rm X"
	// followed by apply_patch "Add X"). Read-only tools return false.
	NeedsDeterministicOrder() bool
}

// PolicyTool is a Tool whose behavior depends on the tool policy of the
// agent that uses it, such as a shell tool that checks the commands it
// runs, or the skill tool that hides skills.
type PolicyTool interface {
	// WithPolicy returns a copy of the tool restricted by p.
	WithPolicy(p toolpolicy.Policy) Tool
}

// Registry holds available tools and provides lookup.
// It supports provider-specific overrides: tools can be replaced or
// excluded for a given provider (e.g. "openai", "anthropic").
//
// A Registry may grow after creation: MCP servers connect in the
// background and Add their tools while agents are already reading the
// registry, so all access is guarded. Tools added mid-conversation are
// picked up at the start of the next Run.
type Registry struct {
	mu        sync.RWMutex
	tools     map[string]Tool
	overrides map[string]map[string]Tool // provider -> name -> Tool (nil = exclude)
	// replacements maps provider -> excluded base name -> replacement name.
	// It records the pairing established by RegisterReplacement so an
	// excluded base tool can hint the caller toward its replacement.
	replacements map[string]map[string]string
}

// NewRegistry creates a Registry from the given tools.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{
		tools:        make(map[string]Tool, len(tools)),
		overrides:    make(map[string]map[string]Tool),
		replacements: make(map[string]map[string]string),
	}
	for _, t := range tools {
		r.tools[t.Definition().Function.Name] = t
	}
	return r
}

// Add registers additional base tools. An existing tool with the same
// name is replaced.
func (r *Registry) Add(tools ...Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range tools {
		r.tools[t.Definition().Function.Name] = t
	}
}

// Remove unregisters the named base tools. Provider overrides are kept.
// Unknown names are ignored.
func (r *Registry) Remove(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		delete(r.tools, name)
	}
}

// RegisterOverrides adds provider-specific tool entries. If the tool
// name matches a base tool it replaces it for that provider; otherwise
// the tool is added only for that provider.
func (r *Registry) RegisterOverrides(provider string, tools ...Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerOverridesLocked(provider, tools...)
}

func (r *Registry) registerOverridesLocked(provider string, tools ...Tool) {
	m := r.overrides[provider]
	if m == nil {
		m = make(map[string]Tool, len(tools))
		r.overrides[provider] = m
	}
	for _, t := range tools {
		m[t.Definition().Function.Name] = t
	}
}

// RegisterExclusions marks base tools as excluded for the given provider.
func (r *Registry) RegisterExclusions(provider string, names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerExclusionsLocked(provider, names...)
}

func (r *Registry) registerExclusionsLocked(provider string, names ...string) {
	m := r.overrides[provider]
	if m == nil {
		m = make(map[string]Tool, len(names))
		r.overrides[provider] = m
	}
	for _, name := range names {
		m[name] = nil
	}
}

// RegisterReplacement records that, for the given provider, the base tool
// baseName is excluded and replaced by replacement. It adds replacement as a
// provider override, excludes baseName, and records the pairing so
// ReplacementFor can later hint a caller that invokes the excluded base tool.
func (r *Registry) RegisterReplacement(provider, baseName string, replacement Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerOverridesLocked(provider, replacement)
	r.registerExclusionsLocked(provider, baseName)
	m := r.replacements[provider]
	if m == nil {
		m = make(map[string]string)
		r.replacements[provider] = m
	}
	m[baseName] = replacement.Definition().Function.Name
}

// ReplacementFor returns the name of the tool that replaces an excluded base
// tool for the given provider, or an empty string if there is no recorded
// replacement.
func (r *Registry) ReplacementFor(name, provider string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.replacements[provider][name]
}

// resolvedTools returns the merged tool map for the given provider.
// Empty provider returns base tools only. The returned map is a copy
// owned by the caller.
func (r *Registry) resolvedTools(provider string) map[string]Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	merged := make(map[string]Tool, len(r.tools))
	maps.Copy(merged, r.tools)
	for name, tool := range r.overrides[provider] {
		if tool == nil {
			delete(merged, name)
		} else {
			merged[name] = tool
		}
	}
	return merged
}

// Overrides is a snapshot of a Registry's provider overrides
// (including exclusions) and replacement pairings. It is produced by
// Registry.Overrides and consumed by Registry.AddOverrides.
type Overrides struct {
	tools        map[string]map[string]Tool
	replacements map[string]map[string]string
}

// Overrides returns a snapshot of r's provider overrides.
func (r *Registry) Overrides() Overrides {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o := Overrides{
		tools:        make(map[string]map[string]Tool, len(r.overrides)),
		replacements: make(map[string]map[string]string, len(r.replacements)),
	}
	for provider, tools := range r.overrides {
		o.tools[provider] = maps.Clone(tools)
	}
	for provider, repl := range r.replacements {
		o.replacements[provider] = maps.Clone(repl)
	}
	return o
}

// AddOverrides merges a snapshot of provider overrides into r. Existing
// entries for the same provider+name are replaced.
func (r *Registry) AddOverrides(o Overrides) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for provider, tools := range o.tools {
		m := r.overrides[provider]
		if m == nil {
			m = make(map[string]Tool, len(tools))
			r.overrides[provider] = m
		}
		maps.Copy(m, tools)
	}
	for provider, repl := range o.replacements {
		m := r.replacements[provider]
		if m == nil {
			m = make(map[string]string, len(repl))
			r.replacements[provider] = m
		}
		maps.Copy(m, repl)
	}
}

// Get returns the tool with the given name, resolved for provider.
// Empty provider uses base tools only.
func (r *Registry) Get(name, provider string) (Tool, bool) {
	resolved := r.resolvedTools(provider)
	t, ok := resolved[name]
	return t, ok
}

// WithPolicy returns a Registry with the tools of r that p allows, each
// tool implementing PolicyTool restricted by p. A zero policy returns r
// itself, so that tools added to r later still show.
//
// Provider overrides are filtered the same way. Provider exclusions of the
// tools p names explicitly are dropped, so that an agent that asks for a
// tool gets it, unless the exclusion makes way for a replacement the
// agent keeps: an agent allowed "bash" gets only exec_command on OpenAI,
// as the main chat does.
func (r *Registry) WithPolicy(p toolpolicy.Policy) *Registry {
	if p.IsZero() {
		return r
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	restrict := func(t Tool) Tool {
		if pt, ok := t.(PolicyTool); ok {
			return pt.WithPolicy(p)
		}
		return t
	}
	filtered := &Registry{
		tools:        make(map[string]Tool, len(r.tools)),
		overrides:    make(map[string]map[string]Tool),
		replacements: make(map[string]map[string]string),
	}
	for name, t := range r.tools {
		if p.AllowsTool(name) {
			filtered.tools[name] = restrict(t)
		}
	}
	for provider, provTools := range r.overrides {
		for name, tool := range provTools {
			if !p.AllowsTool(name) {
				continue
			}
			if tool == nil {
				repl, replaced := r.replacements[provider][name]
				if p.NamesTool(name) && (!replaced || !p.AllowsTool(repl)) {
					continue
				}
			} else {
				tool = restrict(tool)
			}
			if filtered.overrides[provider] == nil {
				filtered.overrides[provider] = make(map[string]Tool)
			}
			filtered.overrides[provider][name] = tool
		}
	}
	for provider, provRepl := range r.replacements {
		for baseName, replName := range provRepl {
			if !p.AllowsTool(replName) {
				continue
			}
			if filtered.replacements[provider] == nil {
				filtered.replacements[provider] = make(map[string]string)
			}
			filtered.replacements[provider][baseName] = replName
		}
	}
	return filtered
}

// Tools returns llmapi.Tool definitions for all registered tools,
// resolved for provider, sorted by name for deterministic ordering.
// Stable ordering is critical for prompt caching — the cache
// breakpoint is on the last tool definition.
func (r *Registry) Tools(provider string) []llmapi.Tool {
	resolved := r.resolvedTools(provider)
	ret := make([]llmapi.Tool, 0, len(resolved))
	for _, t := range resolved {
		ret = append(ret, t.Definition())
	}
	sortTools(ret)
	return ret
}

// AllTools returns llmapi.Tool definitions for all base tools,
// ignoring provider overrides, sorted by name.
func (r *Registry) AllTools() []llmapi.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ret := make([]llmapi.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		ret = append(ret, t.Definition())
	}
	sortTools(ret)
	return ret
}

func sortTools(tools []llmapi.Tool) {
	slices.SortFunc(tools, func(a, b llmapi.Tool) int {
		return strings.Compare(a.Function.Name, b.Function.Name)
	})
}

// EventType describes the kind of event emitted by the agent loop.
type EventType int

const (
	// EventText is a streamed text chunk from the assistant.
	EventText EventType = iota // Streamed text chunk from assistant
	// EventToolCall reports that the agent is invoking a tool.
	EventToolCall // Agent is invoking a tool
	// EventToolResult reports that a tool finished executing.
	EventToolResult // Tool finished executing
	// EventDone reports that the agent finished.
	EventDone // Agent finished (final response complete)
	// EventError reports that an error occurred.
	EventError // Error occurred
	// EventReasoning is a streamed reasoning chunk from reasoning models.
	EventReasoning // Streamed reasoning chunk from reasoning models
	// EventCompacting indicates compaction is in progress.
	EventCompacting // Compaction in progress (animation hint)
	// EventCompacted reports that compaction finished and a new dialogue was created.
	EventCompacted // Compaction done, new dialogue created
	// EventRateLimitWarning reports a provider rate limit warning.
	EventRateLimitWarning // Rate limit warning from provider
	// EventUsageUpdate reports cumulative token usage after a completion.
	EventUsageUpdate // Cumulative token usage update after each completion
	// EventInferenceStart reports that an inference request is about to be sent.
	EventInferenceStart // Inference request about to be sent to LLM
	// EventInferenceReady reports that the inference stream is ready.
	EventInferenceReady // Inference stream created, waiting for first token
	// EventFirstContent reports receipt of the first content token.
	EventFirstContent // First content token received from LLM
	// EventToolsStart reports that tool execution is beginning.
	EventToolsStart // Tool execution phase beginning
	// EventToolsDropped reports that tool results were dropped from history.
	EventToolsDropped // Tool results were dropped from history
	// EventMemoryRecall reports that memory recall completed for the conversation.
	EventMemoryRecall // A memory was recalled for this conversation
	// EventRefusal reports that the model declined to continue (provider
	// refusal stop reason). Terminal, distinct from EventDone/EventError so
	// the TUI can render a refusal banner.
	EventRefusal // Model refused to continue
)

// Event is emitted by the agent loop to drive the TUI.
type Event struct {
	Type               EventType
	Text               string                // For EventText: the text chunk
	Reasoning          string                // For EventReasoning: reasoning chunk
	FinishReason       llmapi.FinishReason   // For EventDone: why the turn ended
	ToolCallID         string                // For EventToolCall/EventToolResult: unique call identifier
	ToolName           string                // For EventToolCall/EventToolResult
	ToolArgs           string                // For EventToolCall: JSON arguments
	ToolSummary        string                // For EventToolCall/EventToolResult: short human-readable args summary
	ToolOutput         string                // For EventToolResult: execution output
	IsError            bool                  // For EventToolResult: was it an error?
	ToolStartTime      time.Time             // For EventToolCall: when the tool call was announced
	ToolDuration       time.Duration         // For EventToolResult: how long the tool took
	Error              error                 // For EventError
	RateLimit          *llmapi.RateLimitInfo // For EventRateLimitWarning
	Usage              llmapi.DialogueUsage  // For EventUsageUpdate: cumulative token usage
	DroppedToolCallIDs []string              // For EventToolsDropped: tool call IDs removed from history
	ArchivedDialogueID string                // For EventCompacted: ID under which old messages were archived
	Context            ContextSnapshot       // For EventUsageUpdate/EventDone: point-in-time context state
	Memories           []Memory              // For EventMemoryRecall: recalled memories
	MemoryDuration     time.Duration         // For EventMemoryRecall: how long recall took
}

// ContextSnapshot captures the point-in-time state of the conversation
// context at the moment an event is emitted.
type ContextSnapshot struct {
	TokensSent     int // Tokens sent to the model (conversation history + request)
	TokensReceived int // Tokens received from the model (completion output)
	Window         int // Model's context window size
	AutoCompactAt  int // Token count at which auto-compaction triggers
}
