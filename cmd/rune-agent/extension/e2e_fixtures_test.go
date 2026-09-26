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

package extension

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
	"unstable.build/rune/internal/debug"
)

// recordingWindowManager is a stub browserapi.WindowManager that records the
// arguments passed to Tab so tests can verify the visible label, the number
// of SetWindowContent calls, and every SetTabActivity call so tests can
// follow a chat tab's activity.
type recordingWindowManager struct {
	mu         sync.Mutex
	gotURI     workspaceapi.URI
	gotIcon    rune
	gotName    string
	gotHandler browserapi.Handler

	setContentCalls int

	activityMu  sync.Mutex
	activity    []tabActivity
	activityErr error

	renames []tabRename
}

type tabActivity struct {
	uri    workspaceapi.URI
	active bool
}

type tabRename struct {
	uri  workspaceapi.URI
	name string
}

func (m *recordingWindowManager) Focus() (browserapi.Window, error) { return nil, nil }
func (m *recordingWindowManager) Split(
	_ browserapi.Orientation, _ browserapi.Window, _ browserapi.Handler,
) (browserapi.Window, error) {
	return nil, nil
}
func (m *recordingWindowManager) Floating(
	_ browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return nil, nil
}
func (m *recordingWindowManager) Bar(_ browserapi.BarConfig, _ tui.Handler) error { return nil }
func (m *recordingWindowManager) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	m.gotURI = uri
	m.gotIcon = icon
	m.gotName = name
	m.gotHandler = h
	return h, nil
}
func (m *recordingWindowManager) SetWindowContent(_ browserapi.Window, _ browserapi.Handler) error {
	m.setContentCalls++
	return nil
}
func (m *recordingWindowManager) CloseWindow(_ browserapi.Window) error { return nil }
func (m *recordingWindowManager) SetTabName(uri workspaceapi.URI, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renames = append(m.renames, tabRename{uri: uri, name: name})
	return nil
}

func (m *recordingWindowManager) Renames() []tabRename {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tabRename, len(m.renames))
	copy(out, m.renames)
	return out
}

func (m *recordingWindowManager) SetTabActivity(uri workspaceapi.URI, active bool) error {
	m.activityMu.Lock()
	defer m.activityMu.Unlock()
	m.activity = append(m.activity, tabActivity{uri: uri, active: active})
	return m.activityErr
}

func (m *recordingWindowManager) tabActivity() []tabActivity {
	m.activityMu.Lock()
	defer m.activityMu.Unlock()
	return slices.Clone(m.activity)
}

// stubNotifications is a no-op browserapi.Notifications. wrapDialogueHandler
// calls Notify(LevelInfo, "canceled completion request") when Ctrl-C cancels
// an in-flight completion; the test does not assert on it.
type stubNotifications struct{}

func (stubNotifications) Notify(_ browserapi.NotificationLevel, _ string, _ ...any) (string, error) {
	return "", nil
}
func (stubNotifications) NotifyOnce(_ browserapi.NotificationLevel, _ string, _ ...any) (string, error) {
	return "", nil
}
func (stubNotifications) UpdateNotificationProgress(_, _ string, _, _ int64) error { return nil }

// memDialogueStore is the minimum dialoguemanager.Store needed to drive the
// agent loop in tests.
type memDialogueStore struct {
	mu        sync.Mutex
	dialogues map[string]dialoguemanager.Dialogue
}

func newMemDialogueStore() *memDialogueStore {
	return &memDialogueStore{dialogues: make(map[string]dialoguemanager.Dialogue)}
}

func (s *memDialogueStore) Health(context.Context) error { return nil }

func (s *memDialogueStore) Create(_ context.Context, d dialoguemanager.Dialogue) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.dialogues[d.ID]; ok {
		return storageapi.ErrAlreadyExists
	}
	s.dialogues[d.ID] = d
	return nil
}

func (s *memDialogueStore) Get(_ context.Context, id string) (dialoguemanager.Dialogue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.dialogues[id]
	if !ok {
		return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
	}
	return d, nil
}

func (s *memDialogueStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dialogues, id)
	return nil
}

func (s *memDialogueStore) AppendMessages(
	_ context.Context, d dialoguemanager.Dialogue, msgs []llmapi.Message, _ llmapi.DialogueUsage,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.dialogues[d.ID]
	existing.Messages = append(existing.Messages, msgs...)
	s.dialogues[d.ID] = existing
	return nil
}

func (s *memDialogueStore) List(
	context.Context,
) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	return iterator.FromSlice[dialoguemanager.DialogueHeader](nil), nil
}

func (s *memDialogueStore) ArchiveAndReplace(
	context.Context, dialoguemanager.ArchiveAndReplaceParams,
) error {
	return nil
}

func (s *memDialogueStore) SetTitle(_ context.Context, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.dialogues[id]
	if !ok {
		return storageapi.ErrNotFound
	}
	d.Title = title
	s.dialogues[id] = d
	return nil
}

// promptFlusher wraps a wrapped chat tui.Handler with an async barrier
// that drains the dialogue handler's consumeIncoming goroutine and the
// scripted agent loop after every Handle. This lets
// handlertest.RunHandlerSequence drive the public handler interface
// (Handle/Draw/Cursor/Resize) and assert against the rendered frame the
// user would see — no private fields or component methods are touched.
type promptFlusher struct {
	t           *testing.T
	inner       tui.Handler
	interruptCh <-chan struct{}
	settle      time.Duration
}

func (a *promptFlusher) Handle(ev term.Event) (exit, handled bool) {
	exit, handled = a.inner.Handle(ev)
	a.flush()
	return
}

func (a *promptFlusher) Resize(w, h int)    { a.inner.Resize(w, h) }
func (a *promptFlusher) Draw(w term.Writer) { a.inner.Draw(w) }
func (a *promptFlusher) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return a.inner.Cursor()
}
func (a *promptFlusher) Selection() (string, bool) { return a.inner.Selection() }

// flush waits for the dialogue tx consumer to publish at least one
// interrupt and then for it to stay quiet for the settle window, so
// any cascading agent-loop events triggered by the most recent input
// have rendered before the next assertion runs.
//
// The first interrupt may arrive after the agent loop reaches the
// prompter and the prompter sends the prompt event on tx, which can
// involve several goroutine hops. The first-event grace window is
// therefore generous; subsequent events use the shorter settle window
// to detect quiescence.
func (a *promptFlusher) flush() {
	const firstEventGrace = 500 * time.Millisecond
	deadline := time.After(3 * time.Second)
	gotAny := false
	for {
		grace := a.settle
		if !gotAny {
			grace = firstEventGrace
		}
		select {
		case <-a.interruptCh:
			gotAny = true
		case <-time.After(grace):
			return
		case <-deadline:
			a.t.Logf("promptFlusher: timed out draining interrupt channel")
			return
		}
	}
}

// nopFileSystem mirrors the helper from the agent package; redeclared here
// so the extension package does not depend on agent test files.
type nopFileSystem struct{}

func (nopFileSystem) URI(string) (workspaceapi.URI, error) {
	return workspaceapi.URI{}, nil
}
func (nopFileSystem) OpenFile(string, int, os.FileMode) (workspaceapi.File, error) {
	return nil, os.ErrNotExist
}
func (nopFileSystem) Remove(string) error                   { return nil }
func (nopFileSystem) Stat(string) (os.FileInfo, error)      { return nil, os.ErrNotExist }
func (nopFileSystem) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (nopFileSystem) MkdirAll(string, os.FileMode) error    { return nil }

func dirURI(dir string) workspaceapi.URI {
	u, _ := workspaceapi.ParseURI("file://" + dir)
	return u
}

// promptHandlerOpts configures the e2e handler fixture.
type promptHandlerOpts struct {
	// toolArgs is the JSON payload the scripted llmapi.Service emits as
	// the ask_user_question arguments. When empty the agent loop is
	// never started (used by tests that drive prompt events directly).
	toolArgs string
	// extraPrompt, when set, is sent on the dialogue tx channel during
	// setup. Tests that exercise the PromptInputMode branch use this
	// to install a RequiresInput option that ask_user_question itself
	// never produces.
	extraPrompt *dialoguetui.MessageEvent
}

// newPromptHandler wires a stub llmapi.Service through agent.Agent →
// ask_user_question → tuiPrompter → dialoguetui → wrapDialogueHandler
// and returns the resulting tui.Handler wrapped in promptFlusher so
// handlertest.RunHandlerSequence can drive it deterministically.
func newPromptHandler(t *testing.T, opts promptHandlerOpts) tui.Handler {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	// Stub llmapi.Service: one scripted tool-call response, followed by
	// a stop reply so the agent loop terminates after the tool returns
	// "prompt dismissed".
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-1",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "ask_user_question",
					Arguments: opts.toolArgs,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	// The interrupter doubles as a barrier: every time the dialogue
	// consumer publishes one (after applying a MessageEvent), the
	// promptFlusher sees it and continues.
	interruptCh := make(chan struct{}, 64)
	interrupter := term.FuncInterrupter(func(context.Context) error {
		select {
		case interruptCh <- struct{}{}:
		default:
		}
		return nil
	})

	comp := dialoguetui.NewComponent(dialoguetui.ComponentConfig{})
	mu := new(sync.Mutex)
	dhandler, tx, rx := dialoguetui.Handler(ctx, mu, comp, interrupter)

	owner := &aiEditorHandler{n: stubNotifications{}, p: interrupter}
	syncComp := syncComponent{mu: mu, comp: comp, h: owner}

	prompter := &tuiPrompter{tx: tx, noti: stubNotifications{}, status: syncComp}
	askUser := agentools.NewAskUser(prompter)
	registry := agent.NewRegistry(askUser)
	skillReg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
	store := newMemDialogueStore()
	ag := agent.NewAgent(svc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000},
		Prompter:     prompter,
	})

	wrapped, msgRx := owner.wrapDialogueHandler(ctx, syncComp, dhandler, rx, "e2e-fixture")

	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-msgRx:
				if !ok {
					return
				}
				it := ag.Run(req.ctx, "test-dialogue", req.modelText)
				for {
					if _, more := it.Next(req.ctx); !more {
						break
					}
				}
				_ = it.Close()
			}
		}
	})

	if opts.extraPrompt != nil {
		tx <- *opts.extraPrompt
		// Wait for the dialogue handler's consumer to apply the event
		// before returning. Without this, the first key the test sends
		// may race ahead of the prompt becoming active.
		select {
		case <-interruptCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for extra prompt to be consumed")
		}
	}

	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	return &promptFlusher{
		t:           t,
		inner:       wrapped,
		interruptCh: interruptCh,
		settle:      50 * time.Millisecond,
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

const (
	frameWidth  = 40
	frameHeight = 10
)

// blanks builds a row of trailing spaces of frameWidth length.
func blanks() string { return strings.Repeat(" ", frameWidth) }

func pad(s string) string {
	if len(s) >= frameWidth {
		return s
	}
	return s + strings.Repeat(" ", frameWidth-len(s))
}

func frame(lines ...string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pad(l)
	}
	return strings.Join(out, "\n")
}

// applyPatchResponse scripts one apply_patch tool call that creates a
// new file via an Add File patch so the tool reports it as touched and
// the agent's auto-diagnostics candidate selection fires.
func applyPatchResponse(callID, path string) llmtest.Response {
	patch := "*** Begin Patch\n*** Add File: " + path + "\n+print('hi')\n*** End Patch"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	return llmtest.Response{
		ToolCalls: []llmapi.ToolCall{{
			ID:   callID,
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionCall{
				Name:      "apply_patch",
				Arguments: string(args),
			},
		}},
		FinishReason: llmapi.FinishReasonToolCall,
	}
}

// assistantSyntheticDiagCalls counts auto-injected check_file_errors
// tool calls (their IDs are prefixed "auto-diag-") carried on assistant
// messages in a captured request.
func assistantSyntheticDiagCalls(msgs []llmapi.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role != llmapi.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "check_file_errors" &&
				strings.HasPrefix(tc.ID, "auto-diag-") {
				n++
			}
		}
	}
	return n
}

// utf8E2EHandler wires the real chat tab handler to a scripted
// llmapi.Service that emits a sequence of read_file / grep_files tool
// calls covering each Layer of the RUNE-179 fix. It returns the
// wrapped tui.Handler plus the scripted service so the test can
// inspect the captured request log after the sequence completes.
func utf8E2EHandler(t *testing.T, svc *llmtest.Service, workspaceDir string) tui.Handler {
	return agentE2EHandler(t, svc, workspaceDir, nil)
}

// agentE2EHandler wires a scripted llmapi.Service through the full agent
// loop against a real on-disk workspace, optionally installing a stub
// LSP so tests can exercise check_file_errors behavior. When lsp is nil
// the agent runs without a language server, matching the UTF-8 fixture.
func agentE2EHandler(t *testing.T, svc *llmtest.Service, workspaceDir string, lsp semanticapi.LSP) tui.Handler {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	interruptCh := make(chan struct{}, 64)
	interrupter := term.FuncInterrupter(func(context.Context) error {
		select {
		case interruptCh <- struct{}{}:
		default:
		}
		return nil
	})

	comp := dialoguetui.NewComponent(dialoguetui.ComponentConfig{})
	mu := new(sync.Mutex)
	dhandler, _, rx := dialoguetui.Handler(ctx, mu, comp, interrupter)

	cwd, err := workspaceapi.ParseURI("file://" + workspaceDir)
	require.NoError(t, err)

	fs := testLocalFS{root: workspaceDir}
	tools, tracker := agentools.DefaultTools(
		fs, testLocalExec{}, cwd, lsp, agentools.Config{}, configedit.NopConfig(),
	)
	// LSP-backed tools (including check_file_errors) are only wired when
	// the fixture installs an LSP; they share DefaultTools' tracker so
	// auto-diagnostics can find the registered check_file_errors tool.
	if lsp != nil {
		tools = append(tools, agentools.LSPTools(lsp, fs, nil, cwd, tracker)...)
	}
	// DefaultTools does not include grep_files (the host-side ripgrep
	// integration covers that path in production). RUNE-179 fixed the
	// in-process grep_files implementation, so register it explicitly
	// for this e2e fixture.
	tools = append(tools, agentools.NewGrepFiles(fs, cwd, agentools.NewFileTracker()))
	registry := agent.NewRegistry(tools...)
	skillReg := skills.NewRegistry(fs, cwd, nil, nil)
	store := newMemDialogueStore()
	ag := agent.NewAgent(svc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000},
		Workspace:    cwd,
	})

	owner := &aiEditorHandler{n: stubNotifications{}, p: interrupter}
	syncComp := syncComponent{mu: mu, comp: comp, h: owner}
	wrapped, msgRx := owner.wrapDialogueHandler(ctx, syncComp, dhandler, rx, "e2e-fixture")

	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-msgRx:
				if !ok {
					return
				}
				it := ag.Run(req.ctx, "test-dialogue", req.modelText)
				for {
					if _, more := it.Next(req.ctx); !more {
						break
					}
				}
				_ = it.Close()
			}
		}
	})

	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	return &promptFlusher{
		t:           t,
		inner:       wrapped,
		interruptCh: interruptCh,
		// Tool execution takes longer than ask_user prompts; bump the
		// quiescence window so the multi-tool scripted sequence
		// finishes before the test asserts on captured requests.
		settle: 150 * time.Millisecond,
	}
}

// findToolResult scans messages for the tool-role message that carries
// the result of the given tool-call ID. Returns ("", false) if absent.
func findToolResult(msgs []llmapi.Message, toolCallID string) (string, bool) {
	for _, m := range msgs {
		if m.Role == llmapi.RoleTool && m.ToolCallID == toolCallID {
			return m.Content, true
		}
	}
	return "", false
}

// nopSpawner is an agent.Spawner that is never expected to run: the
// scripted stop-reason fixtures emit no sub-agent tool calls.
type nopSpawner struct{}

func (nopSpawner) Run(context.Context, agent.RunRequest) (agent.RunHandle, error) {
	return agent.RunHandle{}, nil
}

// stopReasonE2EHandler wires the real floating chat handler through the
// production createAgentCompletions consumer — the same path
// handleChat builds — so tests exercise the agent-loop stop-reason
// handling (pause_turn resume, refusal) exactly as the shipped "agent"
// command does. Only llmapi.Service is stubbed.
func stopReasonE2EHandler(t *testing.T, svc *llmtest.Service) tui.Handler {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	interruptCh := make(chan struct{}, 64)
	interrupter := term.FuncInterrupter(func(context.Context) error {
		select {
		case interruptCh <- struct{}{}:
		default:
		}
		return nil
	})

	comp := dialoguetui.NewComponent(dialoguetui.ComponentConfig{})
	mu := new(sync.Mutex)
	dhandler, tx, rx := dialoguetui.Handler(ctx, mu, comp, interrupter)

	registry := agent.NewRegistry()
	skillReg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
	store := newMemDialogueStore()
	const dialogueID = "test-dialogue"
	ag := agent.NewAgent(svc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000},
	})

	owner := &aiEditorHandler{n: stubNotifications{}, p: interrupter}
	syncComp := syncComponent{mu: mu, comp: comp, h: owner}
	wrapped, msgRx := owner.wrapDialogueHandler(ctx, syncComp, dhandler, rx, "e2e-fixture")

	childEvents := make(chan agent.ChildEvent)
	go debug.CapturePanicReport(func() {
		createAgentCompletions(ctx, cancel, tx, msgRx, ag, nopSpawner{},
			childEvents, skillReg, dialogueID, syncComp, stubNotifications{}, nil, store)
	})

	t.Cleanup(cancel)

	return &promptFlusher{
		t:           t,
		inner:       wrapped,
		interruptCh: interruptCh,
		settle:      100 * time.Millisecond,
	}
}

// noopAgentPrompter is an agent.Prompter that never blocks; the
// sub-agent fixture wires no tools that prompt the user.
type noopAgentPrompter struct{}

func (noopAgentPrompter) Prompt(
	context.Context, agent.PromptRequest,
) (agent.PromptResponse, error) {
	return agent.PromptResponse{}, nil
}

// strictProviderService wraps a llmtest.Service so GetModel honours the
// Provider field, matching the host llmrouter contract that rejects a
// bare model name shared by more than one provider. The default
// llmtest.Service matches on Name alone and would hide the ambiguity
// the sub-agent spawn path must avoid.
type strictProviderService struct {
	*llmtest.Service
	models []llmapi.ModelEntry
}

func (s *strictProviderService) GetModel(
	_ context.Context, m llmapi.ModelEntry,
) (llmapi.ModelEntry, error) {
	if m.Provider == "" {
		return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
	}
	for _, e := range s.models {
		if e.Name == m.Name && e.Provider == m.Provider {
			return e, nil
		}
	}
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

// subAgentSpawnE2EHandler wires the real floating chat handler through
// the production createAgentCompletions consumer with a real
// agent.GoroutineSpawner and the "agent" tool registered, so the agent
// tool call spawns a sub-agent exactly as the shipped "agent" command
// does. Only the llmapi.Service and the spawner's service factory are
// stubbed.
func subAgentSpawnE2EHandler(
	t *testing.T,
	svc *llmtest.Service,
	serviceFactory agent.ServiceFactory,
	model llmapi.ModelEntry,
) (tui.Handler, *agent.Agent) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	interruptCh := make(chan struct{}, 64)
	interrupter := term.FuncInterrupter(func(context.Context) error {
		select {
		case interruptCh <- struct{}{}:
		default:
		}
		return nil
	})

	comp := dialoguetui.NewComponent(dialoguetui.ComponentConfig{})
	mu := new(sync.Mutex)
	dhandler, tx, rx := dialoguetui.Handler(ctx, mu, comp, interrupter)

	skillReg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
	store := newMemDialogueStore()
	const dialogueID = "test-dialogue"
	const agentID = "default"

	cfg := defaultAgentsConfig("test")
	spawner := agent.NewGoroutineSpawner(
		store, serviceFactory, cfg, skillReg, agent.NoMemory(), "",
		dialogueID, agentID, dirURI(""), noopAgentPrompter{},
	)
	childEvents := make(chan agent.ChildEvent, 64)
	sessionTools := agentools.SessionTools(spawner, spawner.ListAgents(), childEvents, skillReg)
	registry := agent.NewRegistry(sessionTools...)
	spawner.SetRegistry(registry)

	ag := agent.NewAgent(svc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        model,
		SessionKey:   dialogueID,
		AgentID:      agentID,
	})

	owner := &aiEditorHandler{n: stubNotifications{}, p: interrupter}
	syncComp := syncComponent{mu: mu, comp: comp, h: owner}
	wrapped, msgRx := owner.wrapDialogueHandler(ctx, syncComp, dhandler, rx, "e2e-fixture")

	go debug.CapturePanicReport(func() {
		createAgentCompletions(ctx, cancel, tx, msgRx, ag, spawner,
			childEvents, skillReg, dialogueID, syncComp, stubNotifications{}, nil, store)
	})

	t.Cleanup(cancel)

	return &promptFlusher{
		t:           t,
		inner:       wrapped,
		interruptCh: interruptCh,
		settle:      150 * time.Millisecond,
	}, ag
}

// maxTokensE2EHandler wires the real floating chat handler with a
// production commandAdapter as its CommandHandler, bound to a concrete
// model whose documented output ceiling is known. It mirrors the
// handleChat wiring (WithCommands(adapter)) so /max_tokens flows through
// the same validation path the shipped "agent" command uses. The agent
// is returned so the test can assert the override was not applied when
// validation rejects the value.
func maxTokensE2EHandler(t *testing.T, model llmapi.ModelEntry) (tui.Handler, *agent.Agent) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	interruptCh := make(chan struct{}, 64)
	interrupter := term.FuncInterrupter(func(context.Context) error {
		select {
		case interruptCh <- struct{}{}:
		default:
		}
		return nil
	})

	svc := llmtest.New([]llmapi.ModelEntry{model})
	comp := dialoguetui.NewComponent(dialoguetui.ComponentConfig{})
	mu := new(sync.Mutex)

	registry := agent.NewRegistry()
	skillReg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
	store := newMemDialogueStore()
	ag := agent.NewAgent(svc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        model,
	})

	adapter := &commandAdapter{agent: ag, skillRegistry: skillReg}
	dhandler, _, rx := dialoguetui.Handler(ctx, mu, comp, interrupter,
		dialoguetui.WithCommands(adapter),
	)

	owner := &aiEditorHandler{n: stubNotifications{}, p: interrupter}
	syncComp := syncComponent{mu: mu, comp: comp, h: owner}
	wrapped, _ := owner.wrapDialogueHandler(ctx, syncComp, dhandler, rx, "e2e-fixture")

	t.Cleanup(cancel)

	return &promptFlusher{
		t:           t,
		inner:       wrapped,
		interruptCh: interruptCh,
		settle:      100 * time.Millisecond,
	}, ag
}
