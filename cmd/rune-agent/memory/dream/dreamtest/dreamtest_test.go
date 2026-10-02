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

// Package dreamtest exercises the dream command end-to-end through a
// real repl.Handler and a dream command handler that runs the real
// dream.Dream pipeline against a stubbed llmapi.Service. It streams one
// styled dreamcomponent.New per dream.Progress event the same way
// agentshell does in production, and asserts the resulting lifecycle
// (bootstrap → phases → analyzing → tool results → done) as a
// sequence of handlertest.RunHandlerSequence golden frames.
package dreamtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/memory/dream"
	"unstable.build/rune/cmd/rune-agent/memory/dream/dreamcomponent"
	"unstable.build/rune/internal/term/sh"
)

func TestDream_E2E_NoDialogues(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOpts{})

	handlertest.RunHandlerSequence(t, f.flush, testWidth, testHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "dream<enter>",
			Expected: f.expected(6,
				"dream> dream",
				"✓ initializing memory workspace",
				"⚙ Phase: Extract memories from conversations",
				"✓ Phase: Extract memories from conversations",
				"■ Dream complete",
				"dream> \u2590",
			),
		},
	})

	// No dialogue → no Total>0 progress events → ProgressWriter never
	// gets called.
	require.Zero(t, f.pwCallCount(), "pw.Progress should not be called when there are no dialogues")
}

func TestDream_E2E_SingleDialogueWithToolCall(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOpts{
		dialogues: []dialoguemanager.Dialogue{{
			ID:      "d1",
			Version: 1,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "How should I test repls?"},
				{Role: llmapi.RoleAssistant, Content: "Use handlertest.RunHandlerSequence."},
			},
		}},
		responses: []mockLLMResponse{
			toolCallResponse("call_1", "read_file", `{"path":"main.go"}`),
			stopResponse("Memories extracted."),
		},
	})

	handlertest.RunHandlerSequence(t, f.flush, testWidth, testHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "dream<enter>",
			Expected: f.expected(0,
				"dream> dream",
				"✓ initializing memory workspace",
				"⚙ Phase: Extract memories from conversations",
				"⚙ conversation \"d1\" (1/1 conversations)",
				"⚙ extracting memories (1/1 conversations)",
				"└─ ✗ read_file (in 250ms)",
				"⚙ verifying memories (1/1 conversations)",
				"✓ Phase: Extract memories from conversations",
				"⚙ Phase: Refine recall machinery",
				"✓ Phase: Refine recall machinery",
				"■ Dream complete",
				"dream> \u2590",
			),
		},
	})

	// New-dialogue events now carry the pre-counted Total so the REPL
	// progress bar can render meaningful percentages.
	require.NotZero(t, f.pwCallCount(),
		"new-dialogue events should report Total>0 via ProgressWriter")
}

// TestDream_E2E_LifecycleFrames captures the dream lifecycle across
// multiple draws: while dream is mid-run the component shows the
// working glyph, then flips to ✓ once the matching finish event lands.
// The transition is driven by a redraw key (<c-l>) after the first
// command has completed.
func TestDream_E2E_LifecycleFrames(t *testing.T) {
	t.Parallel()
	opts := fixtureOpts{
		dialogues: []dialoguemanager.Dialogue{{
			ID:      "d1",
			Version: 1,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "What is dream?"},
				{Role: llmapi.RoleAssistant, Content: "Memory consolidation."},
			},
		}},
		responses: []mockLLMResponse{
			stopResponse("Memories extracted."),
		},
	}
	f := newFixtureWithLLM(t, opts, &mockLLMService{
		responses:     opts.responses,
		responseDelay: 25 * time.Millisecond,
	})

	handlertest.RunHandlerSequence(t, f.flush, testWidth, testHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "dream<enter>",
			Expected: f.expected(1,
				"dream> dream",
				"✓ initializing memory workspace",
				"⚙ Phase: Extract memories from conversations",
				"⚙ conversation \"d1\" (1/1 conversations)",
				"⚙ extracting memories (1/1 conversations)",
				"⚙ verifying memories (1/1 conversations)",
				"✓ Phase: Extract memories from conversations",
				"⚙ Phase: Refine recall machinery",
				"✓ Phase: Refine recall machinery",
				"■ Dream complete",
				"dream> \u2590",
			),
		},
		{
			// Re-dream after success runs a no-op: bootstrap is noop,
			// extract finds the dialogue already dreamed, refine
			// already ran for this LastExtract epoch. Output from the
			// first run is still visible above the second prompt.
			InputSequence: "dream<enter>",
			Expected: f.expected(0,
				"⚙ extracting memories (1/1 conversations)",
				"⚙ verifying memories (1/1 conversations)",
				"✓ Phase: Extract memories from conversations",
				"⚙ Phase: Refine recall machinery",
				"✓ Phase: Refine recall machinery",
				"■ Dream complete",
				"dream> dream",
				"✓ initializing memory workspace",
				"⚙ Phase: Extract memories from conversations",
				"✓ Phase: Extract memories from conversations",
				"■ Dream complete",
				"dream> \u2590",
			),
		},
	})
}

// TestDream_E2E_InProgress freezes the pipeline inside the first LLM
// call so the component must render the still-running glyph for the
// active conversation entry. Then the LLM is released and the next
// RunHandlerSequence case asserts the completed frame.
func TestDream_E2E_InProgress(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	blocked := make(chan struct{})
	llmSvc := &mockLLMService{
		responses:     []mockLLMResponse{stopResponse("done")},
		blockReleased: release,
		blockEntered:  blocked,
	}
	f := newFixtureWithLLM(t, fixtureOpts{
		drawWhileRunning: blocked,
		dialogues: []dialoguemanager.Dialogue{{
			ID:      "d1",
			Version: 1,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "How should I test dream?"},
				{Role: llmapi.RoleAssistant, Content: "End-to-end."},
			},
		}},
	}, llmSvc)

	// Block the LLM so dream.Dream stops inside runAgent. The component
	// shows the working glyph on the conversation entry.
	handlertest.RunHandlerSequence(t, f.flush, testWidth, testHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "dream<enter>",
			Expected: f.expected(6,
				"dream> dream",
				"✓ initializing memory workspace",
				"⚙ Phase: Extract memories from conversations",
				"⚙ conversation \"d1\" (1/1 conversations)",
				// While dream is mid-run the REPL's built-in
				// progress bar overlays the last output row using
				// the totals fed via pw.Progress.
				"╢░░░░░░░░░░░░░░░░░░░░░░░░░╟ 100% 1/1 conversations",
				"dream> \u2590",
			),
		},
	})

	// Release the LLM. The pump goroutine drains the remaining events
	// and the component flips the entries to ✓ / ■.
	close(release)
	handlertest.RunHandlerSequence(t, f.flush, testWidth, testHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "<c-l>",
			Expected: f.expected(1,
				"dream> dream",
				"✓ initializing memory workspace",
				"⚙ Phase: Extract memories from conversations",
				"⚙ conversation \"d1\" (1/1 conversations)",
				"⚙ extracting memories (1/1 conversations)",
				"⚙ verifying memories (1/1 conversations)",
				"✓ Phase: Extract memories from conversations",
				"⚙ Phase: Refine recall machinery",
				"✓ Phase: Refine recall machinery",
				"■ Dream complete",
				"dream> \u2590",
			),
		},
	})
}

// TestDream_E2E_ViaShellInterp reproduces the live IDE shell flow: the
// REPL CommandHandler is sh.New(registry) and the agentshell-style
// dream command is registered as the "agent" parent command. The user
// types `agent dream<enter>` and we assert the in-place component
// renders its lifecycle frames. This guards against regressions where
// the sh-aware shell middleware drains our iterator differently from
// the REPL's own dispatch.
func TestDream_E2E_ViaShellInterp(t *testing.T) {
	t.Parallel()
	f := newShellFixture(t, fixtureOpts{
		dialogues: []dialoguemanager.Dialogue{{
			ID:      "d1",
			Version: 1,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "What is dream?"},
				{Role: llmapi.RoleAssistant, Content: "Memory consolidation."},
			},
		}},
		responses: []mockLLMResponse{
			stopResponse("Memories extracted."),
		},
	})

	handlertest.RunHandlerSequence(t, f.flush, testWidth, testHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "agent<space>dream<enter>",
			Expected: f.expected(1,
				"> agent dream",
				"✓ initializing memory workspace",
				"⚙ Phase: Extract memories from conversations",
				"⚙ conversation \"d1\" (1/1 conversations)",
				"⚙ extracting memories (1/1 conversations)",
				"⚙ verifying memories (1/1 conversations)",
				"✓ Phase: Extract memories from conversations",
				"⚙ Phase: Refine recall machinery",
				"✓ Phase: Refine recall machinery",
				"■ Dream complete",
				"> \u2590",
			),
		},
	})
}

// --- Fixture ------------------------------------------------------------

const (
	testWidth  = 50
	testHeight = 12
)

type progressCall struct {
	progress, total int64
	units           string
}

type fixtureOpts struct {
	dialogues        []dialoguemanager.Dialogue
	responses        []mockLLMResponse
	drawWhileRunning <-chan struct{}
}

type fixture struct {
	t     *testing.T
	h     *repl.Handler
	flush *asyncFlusher
	sched *syncScheduler

	mu         sync.Mutex
	pwCallsLog []progressCall
}

func newFixture(t *testing.T, opts fixtureOpts) *fixture {
	t.Helper()
	return newFixtureWithLLM(t, opts, &mockLLMService{responses: opts.responses})
}

// newShellFixture wires a fixture whose REPL CommandHandler is
// sh.New over a tiny registry. The dream command is registered under
// the "agent" name to mirror the rune editor's IDE shell, where the
// rune-agent extension exposes agentshell as a single top-level
// command and users type `agent dream`.
func newShellFixture(t *testing.T, opts fixtureOpts) *fixture {
	t.Helper()

	dataPath := newMemoryWorkspace(t)
	store := &mockDialogueStore{dialogues: opts.dialogues}
	storage := storagestub.NewInMemoryService()

	sched := &syncScheduler{}

	f := &fixture{
		t:     t,
		sched: sched,
	}

	dreamCmd := &dreamCommand{
		f: f,
		deps: dream.Deps{
			LLM:           &mockLLMService{responses: opts.responses},
			Store:         store,
			Storage:       storage,
			FS:            osFileSystem{},
			Exec:          &mockExec{},
			LSP:           noopLSP{},
			Parser:        nil,
			Notifications: noopNotifications{},
			DataPath:      dataPath,
			Model:         llmapi.ModelEntry{Name: "test-model", ContextWindow: 100000},
		},
	}

	reg := &shellRegistry{cmds: map[string]repl.CommandHandler{
		"agent": &agentParentCommand{child: dreamCmd},
	}}
	shHandler := sh.New(reg, workspaceapi.URI{})

	f.h = repl.New(shHandler, sched.schedule, term.NopInterrupter(),
		repl.WithPrompt("> "),
		repl.WithRunningAnimationFrames([]string{}, []int{}),
	)
	f.flush = &asyncFlusher{f: f, drawWhileRunning: opts.drawWhileRunning}

	t.Cleanup(func() { _ = f.h.Close() })
	return f
}

// newFixtureWithLLM is like newFixture but lets the caller construct
// the mockLLMService directly (e.g. to wire a release channel).
func newFixtureWithLLM(t *testing.T, opts fixtureOpts, llmSvc *mockLLMService) *fixture {
	t.Helper()

	dataPath := newMemoryWorkspace(t)
	store := &mockDialogueStore{dialogues: opts.dialogues}
	storage := storagestub.NewInMemoryService()

	sched := &syncScheduler{}

	f := &fixture{
		t:     t,
		sched: sched,
	}

	cmd := &dreamCommand{
		f: f,
		deps: dream.Deps{
			LLM:           llmSvc,
			Store:         store,
			Storage:       storage,
			FS:            osFileSystem{},
			Exec:          &mockExec{},
			LSP:           noopLSP{},
			Parser:        nil,
			Notifications: noopNotifications{},
			DataPath:      dataPath,
			Model:         llmapi.ModelEntry{Name: "test-model", ContextWindow: 100000},
		},
	}

	f.h = repl.New(cmd, sched.schedule, term.NopInterrupter(),
		repl.WithPrompt("dream> "),
		// Hide the REPL's running spinner from goldens; dreamcomponent
		// owns the in-place working glyph. An empty sequence makes the
		// REPL spinner's Draw a no-op so it never overlays the last
		// output row (where the in-progress dialogue entry renders).
		repl.WithRunningAnimationFrames([]string{}, []int{}),
	)
	f.flush = &asyncFlusher{f: f, drawWhileRunning: opts.drawWhileRunning}

	t.Cleanup(func() { _ = f.h.Close() })
	return f
}

func (f *fixture) pwCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pwCallsLog)
}

func (f *fixture) recordProgress(p, total int64, units string) {
	f.mu.Lock()
	f.pwCallsLog = append(f.pwCallsLog, progressCall{progress: p, total: total, units: units})
	f.mu.Unlock()
}

func (f *fixture) expected(numBlank int, lines ...string) string {
	out := make([]string, 0, numBlank+len(lines))
	bl := strings.Repeat(" ", testWidth)
	for range numBlank {
		out = append(out, bl)
	}
	for _, l := range lines {
		out = append(out, pad(l))
	}
	return strings.Join(out, "\n")
}

func pad(s string) string {
	n := utf8.RuneCountInString(s)
	if n >= testWidth {
		return s
	}
	return s + strings.Repeat(" ", testWidth-n)
}

// syncScheduler captures scheduleNextTick callbacks and drains them
// synchronously. flush re-drains callbacks scheduled during draining.
type syncScheduler struct {
	mu      sync.Mutex
	pending []func()
}

func (s *syncScheduler) schedule(fn func()) bool {
	s.mu.Lock()
	s.pending = append(s.pending, fn)
	s.mu.Unlock()
	return true
}

func (s *syncScheduler) flush() {
	for {
		s.mu.Lock()
		batch := s.pending
		s.pending = nil
		s.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, fn := range batch {
			fn()
		}
	}
}

// asyncFlusher waits for the REPL command goroutine and then drains all
// scheduleNextTick callbacks before Draw compares the golden frame.
type asyncFlusher struct {
	f                *fixture
	drawWhileRunning <-chan struct{}
}

func (a *asyncFlusher) Handle(ev term.Event) (exit, handled bool) {
	exit, handled = a.f.h.Handle(ev)
	if ev.Key == term.KeyEnter && a.drawWhileRunning != nil {
		<-a.drawWhileRunning
		a.drawWhileRunning = nil
		// drawWhileRunning only means the pipeline reached the blocked
		// LLM call. The in-progress conversation row travels through
		// the REPL pump goroutine on its own time, so poll until it is
		// rendered before the golden compare.
		// (The REPL progress bar also says "conversations", so match
		// the full row prefix.)
		a.waitFrameContains("⚙ conversation ")
	} else {
		a.f.h.Wait()
	}
	a.f.sched.flush()
	return
}

// waitFrameContains flushes scheduled callbacks and redraws until the
// frame contains want.
func (a *asyncFlusher) waitFrameContains(want string) {
	a.f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.f.sched.flush()
		w := term.NewStringWriter(testWidth, testHeight)
		a.f.h.Draw(w)
		if err := w.Flush(); err == nil &&
			strings.Contains(w.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			a.f.t.Fatalf("frame never showed %q", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (a *asyncFlusher) Resize(w, h int)    { a.f.h.Resize(w, h) }
func (a *asyncFlusher) Draw(w term.Writer) { a.f.h.Draw(w) }
func (a *asyncFlusher) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return a.f.h.Cursor()
}
func (a *asyncFlusher) Selection() (string, bool) { return a.f.h.Selection() }

// dreamCommand mirrors agentshell.handleDream: it runs the real
// dream.Dream pipeline, yields one styled Responsive row per
// dream.Progress event, and calls pw.Progress for events with Total>0.
type dreamCommand struct {
	f    *fixture
	deps dream.Deps
}

func (d *dreamCommand) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if cmd.Name != "dream" {
		return iterator.Empty[component.Responsive](), fmt.Errorf("unknown command: %s", cmd.Name)
	}
	it, err := dream.Dream(ctx, d.deps)
	if err != nil {
		return nil, err
	}

	// Mirror the production agentshell.handleDream behavior: stream
	// one styled Responsive row per dream.Progress event, skip the
	// ProgressToolCall events (only ProgressToolResult yields a row),
	// and forward Progress to pw.
	return iterator.FromFunc(
		func(ctx context.Context) (component.Responsive, bool, error) {
			for {
				p, ok := it.Next(ctx)
				if !ok {
					return nil, false, it.Err()
				}
				if p.Total > 0 {
					d.f.recordProgress(int64(p.Progress)+1, int64(p.Total), p.Units)
					pw.Progress(int64(p.Progress)+1, int64(p.Total), p.Units)
				}
				if p.Type == dream.ProgressToolCall {
					continue
				}
				// The tool duration is wall-clock time, so a slow CI
				// runner can tip a sub-millisecond stub call over 1ms
				// and change the rendered suffix. Pin it so the golden
				// frames stay deterministic; fmtDuration's rendering
				// is covered by dreamcomponent's row tests.
				if p.Type == dream.ProgressToolResult {
					p.Duration = 250 * time.Millisecond
				}
				return dreamcomponent.New(p), true, nil
			}
		},
		func() error { return it.Close() },
	), nil
}

func (d *dreamCommand) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// shellRegistry is a tiny repl.CommandHandler used as the sh.New
// underlying registry. It looks up commands by Name and delegates.
type shellRegistry struct {
	cmds map[string]repl.CommandHandler
}

func (r *shellRegistry) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	h, ok := r.cmds[cmd.Name]
	if !ok {
		return nil, repl.ErrNotFound
	}
	return h.HandleCommand(ctx, cmd, pw)
}

func (r *shellRegistry) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// agentParentCommand mirrors agentshell.HandleCommand's behavior of
// re-dispatching `agent <sub> [args]` as `<sub> [args]` against an
// inner command handler.
type agentParentCommand struct {
	child repl.CommandHandler
}

func (a *agentParentCommand) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return iterator.Empty[component.Responsive](), nil
	}
	sub := repl.Command{Name: cmd.Args[0], Args: cmd.Args[1:]}
	return a.child.HandleCommand(ctx, sub, pw)
}

func (a *agentParentCommand) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// newMemoryWorkspace creates a temp directory pre-populated with a
// minimal go.mod and a version marker matching dream's current
// template version. This makes bootstrap return actionNoop so we can
// run the full dream.Dream pipeline without spawning `go mod tidy`.
func newMemoryWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module memories\n"), 0o644))
	// Match dream's current template version. Bumped when dream
	// upgrades its template; readVersion in dream parses base-10 ints.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "version"),
		[]byte("99\n"), 0o644))
	return dir
}

// --- Stubs --------------------------------------------------------------

// mockLLMService implements llmapi.Service for tests. Each
// CreateCompletion returns the next pre-canned response.
type mockLLMService struct {
	mu            sync.Mutex
	callCount     int
	responses     []mockLLMResponse
	responseDelay time.Duration
	blockOnce     sync.Once
	// blockReleased, when non-nil, makes CreateCompletion block on
	// the channel before returning the response. Used to freeze the
	// dream pipeline so tests can assert mid-run frames.
	blockReleased chan struct{}
	blockEntered  chan struct{}
}

type mockLLMResponse struct {
	text         string
	finishReason llmapi.FinishReason
	toolCalls    []llmapi.ToolCall
}

func stopResponse(text string) mockLLMResponse {
	return mockLLMResponse{text: text, finishReason: llmapi.FinishReasonStop}
}

func toolCallResponse(id, name, args string) mockLLMResponse {
	return mockLLMResponse{
		finishReason: llmapi.FinishReasonToolCall,
		toolCalls: []llmapi.ToolCall{{
			ID:       id,
			Type:     llmapi.ToolTypeFunction,
			Function: llmapi.FunctionCall{Name: name, Arguments: args},
		}},
	}
}

func (m *mockLLMService) CreateCompletion(
	ctx context.Context, _ llmapi.ModelEntry, _ llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.blockReleased != nil {
		m.blockOnce.Do(func() {
			if m.blockEntered != nil {
				close(m.blockEntered)
			}
		})
		select {
		case <-m.blockReleased:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.responseDelay > 0 {
		timer := time.NewTimer(m.responseDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m.mu.Lock()
	idx := m.callCount
	m.callCount++
	m.mu.Unlock()

	if idx >= len(m.responses) {
		// Default: stop. Lets the agent loop terminate gracefully if
		// the test under-specifies responses (e.g. retries after a tool
		// failure).
		return iterator.FromSlice([]llmapi.Event{
			{Type: llmapi.EventTextDelta, Text: ""},
			{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
				Message:      llmapi.Message{Role: llmapi.RoleAssistant},
				FinishReason: llmapi.FinishReasonStop,
			}},
		}), nil
	}

	resp := m.responses[idx]
	events := []llmapi.Event{}
	if resp.text != "" {
		events = append(events, llmapi.Event{Type: llmapi.EventTextDelta, Text: resp.text})
	}
	for _, tc := range resp.toolCalls {
		events = append(events, llmapi.Event{Type: llmapi.EventToolCallDone, ToolCall: &tc})
	}
	events = append(events, llmapi.Event{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
		Message: llmapi.Message{
			Role:      llmapi.RoleAssistant,
			Content:   resp.text,
			ToolCalls: resp.toolCalls,
		},
		FinishReason: resp.finishReason,
	}})
	return iterator.FromSlice(events), nil
}

func (m *mockLLMService) CountTokens(_ llmapi.ModelEntry, _ []llmapi.Message) (int, error) {
	return 0, nil
}

func (m *mockLLMService) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice([]llmapi.ModelEntry{{Name: "test-model", ContextWindow: 100000}})
}

func (m *mockLLMService) GetModel(
	_ context.Context, model llmapi.ModelEntry,
) (llmapi.ModelEntry, error) {
	if model.Name == "" {
		model.Name = "test-model"
	}
	if model.ContextWindow == 0 {
		model.ContextWindow = 100000
	}
	return model, nil
}

// mockDialogueStore implements dialoguemanager.Store with an
// in-memory slice of dialogues.
type mockDialogueStore struct {
	mu        sync.Mutex
	dialogues []dialoguemanager.Dialogue
}

func (s *mockDialogueStore) Health(context.Context) error { return nil }

func (s *mockDialogueStore) Create(_ context.Context, d dialoguemanager.Dialogue) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dialogues = append(s.dialogues, d)
	return nil
}

func (s *mockDialogueStore) Get(_ context.Context, id string) (dialoguemanager.Dialogue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.dialogues {
		if d.ID == id {
			return d, nil
		}
	}
	return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
}

func (s *mockDialogueStore) Delete(context.Context, string) error { return nil }

func (s *mockDialogueStore) AppendMessages(
	context.Context, dialoguemanager.Dialogue, []llmapi.Message, llmapi.DialogueUsage,
) error {
	return nil
}

func (s *mockDialogueStore) List(
	context.Context,
) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	headers := make([]dialoguemanager.DialogueHeader, len(s.dialogues))
	for i, d := range s.dialogues {
		headers[i] = d.Header()
	}
	return iterator.FromSlice(headers), nil
}

func (s *mockDialogueStore) ArchiveAndReplace(context.Context, dialoguemanager.ArchiveAndReplaceParams) error {
	return nil
}

func (s *mockDialogueStore) SetTitle(_ context.Context, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.dialogues {
		if d.ID == id {
			s.dialogues[i].Title = title
			return nil
		}
	}
	return storageapi.ErrNotFound
}

// mockExec implements workspaceapi.Executor. Every Start returns
// success without spawning a process — `go test ./...` and
// `go mod tidy` thus pass instantly.
type mockExec struct{}

func (mockExec) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	if cmd.Watcher != nil {
		go func() { cmd.Watcher.WatchProcess() <- nil }()
	}
	return 0, nil
}

func (mockExec) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (mockExec) Close() error                                  { return nil }

// osFileSystem is a real os-backed FileSystem so dream's bootstrap,
// preReadWorkspaceFiles, and the read_file tool see real disk state.
type osFileSystem struct{}

func (osFileSystem) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.CurrentUserHostURI(path)
}

func (osFileSystem) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(path, flag, mode)
}

func (osFileSystem) Remove(path string) error                     { return os.Remove(path) }
func (osFileSystem) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (osFileSystem) ReadDir(name string) ([]os.DirEntry, error)   { return os.ReadDir(name) }
func (osFileSystem) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// noopLSP returns empty results for every query. dream only uses
// WorkspaceSymbol via discoverCategories; the rest is required to
// satisfy semanticapi.LSP.
type noopLSP struct{}

func (noopLSP) Initialize(context.Context, semanticapi.InitializeParams) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}
func (noopLSP) Initialized(context.Context) error { return nil }
func (noopLSP) Shutdown(context.Context) error    { return nil }
func (noopLSP) Exit(context.Context) error        { return nil }
func (noopLSP) DidOpen(context.Context, semanticapi.DidOpenTextDocumentParams) error {
	return nil
}
func (noopLSP) DidChange(context.Context, semanticapi.DidChangeTextDocumentParams) error {
	return nil
}
func (noopLSP) DidClose(context.Context, semanticapi.DidCloseTextDocumentParams) error {
	return nil
}
func (noopLSP) DidSave(context.Context, semanticapi.DidSaveTextDocumentParams) error { return nil }
func (noopLSP) Completion(context.Context, semanticapi.CompletionParams) (semanticapi.CompletionResult, error) {
	return semanticapi.CompletionResult{}, nil
}
func (noopLSP) Hover(context.Context, semanticapi.HoverParams) (*semanticapi.Hover, error) {
	return nil, nil
}
func (noopLSP) SignatureHelp(context.Context, semanticapi.SignatureHelpParams) (*semanticapi.SignatureHelp, error) {
	return nil, nil
}
func (noopLSP) Definition(context.Context, semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) Declaration(context.Context, semanticapi.DeclarationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) TypeDefinition(context.Context, semanticapi.TypeDefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) Implementation(context.Context, semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) References(context.Context, semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
	return nil, nil
}
func (noopLSP) DocumentHighlight(context.Context, semanticapi.DocumentHighlightParams) ([]semanticapi.DocumentHighlight, error) {
	return nil, nil
}
func (noopLSP) DocumentSymbol(context.Context, semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
	return semanticapi.DocumentSymbolResult{}, nil
}
func (noopLSP) CodeAction(context.Context, semanticapi.CodeActionParams) ([]semanticapi.CodeActionResult, error) {
	return nil, nil
}
func (noopLSP) CodeLens(context.Context, semanticapi.CodeLensParams) ([]semanticapi.CodeLens, error) {
	return nil, nil
}
func (noopLSP) Formatting(context.Context, semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) RangeFormatting(context.Context, semanticapi.DocumentRangeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) Rename(context.Context, semanticapi.RenameParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) PrepareRename(context.Context, semanticapi.PrepareRenameParams) (*semanticapi.PrepareRenameResult, error) {
	return nil, nil
}
func (noopLSP) FoldingRange(context.Context, semanticapi.FoldingRangeParams) ([]semanticapi.FoldingRange, error) {
	return nil, nil
}
func (noopLSP) SelectionRange(context.Context, semanticapi.SelectionRangeParams) ([]semanticapi.SelectionRange, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensFull(context.Context, semanticapi.SemanticTokensParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensRange(context.Context, semanticapi.SemanticTokensRangeParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (noopLSP) Diagnostic(context.Context, semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
	return semanticapi.DocumentDiagnosticReport{}, nil
}
func (noopLSP) WorkspaceDiagnostic(context.Context, semanticapi.WorkspaceDiagnosticParams) (semanticapi.WorkspaceDiagnosticReport, error) {
	return semanticapi.WorkspaceDiagnosticReport{}, nil
}
func (noopLSP) WorkspaceSymbol(context.Context, semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
	return nil, nil
}
func (noopLSP) ExecuteCommand(context.Context, semanticapi.ExecuteCommandParams) (string, error) {
	return "", nil
}
func (noopLSP) ExecuteRequest(context.Context, semanticapi.ExecuteRequestParams) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (noopLSP) SendNotification(context.Context, semanticapi.NotificationParams) error {
	return nil
}
func (noopLSP) PrepareCallHierarchy(context.Context, semanticapi.CallHierarchyPrepareParams) ([]semanticapi.CallHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) CallHierarchyIncomingCalls(context.Context, semanticapi.CallHierarchyIncomingCallsParams) ([]semanticapi.CallHierarchyIncomingCall, error) {
	return nil, nil
}
func (noopLSP) CallHierarchyOutgoingCalls(context.Context, semanticapi.CallHierarchyOutgoingCallsParams) ([]semanticapi.CallHierarchyOutgoingCall, error) {
	return nil, nil
}
func (noopLSP) CompletionResolve(context.Context, semanticapi.CompletionItem) (semanticapi.CompletionItem, error) {
	return semanticapi.CompletionItem{}, nil
}
func (noopLSP) CodeLensResolve(context.Context, semanticapi.CodeLens) (semanticapi.CodeLens, error) {
	return semanticapi.CodeLens{}, nil
}
func (noopLSP) DocumentColor(context.Context, semanticapi.DocumentColorParams) ([]semanticapi.ColorInformation, error) {
	return nil, nil
}
func (noopLSP) ColorPresentation(context.Context, semanticapi.ColorPresentationParams) ([]semanticapi.ColorPresentation, error) {
	return nil, nil
}
func (noopLSP) DocumentLink(context.Context, semanticapi.DocumentLinkParams) ([]semanticapi.DocumentLink, error) {
	return nil, nil
}
func (noopLSP) DocumentLinkResolve(context.Context, semanticapi.DocumentLink) (semanticapi.DocumentLink, error) {
	return semanticapi.DocumentLink{}, nil
}
func (noopLSP) OnTypeFormatting(context.Context, semanticapi.DocumentOnTypeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) LinkedEditingRange(context.Context, semanticapi.LinkedEditingRangeParams) (*semanticapi.LinkedEditingRanges, error) {
	return nil, nil
}
func (noopLSP) Moniker(context.Context, semanticapi.MonikerParams) ([]semanticapi.Moniker, error) {
	return nil, nil
}
func (noopLSP) WillSaveWaitUntil(context.Context, semanticapi.WillSaveTextDocumentParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensFullDelta(context.Context, semanticapi.SemanticTokensDeltaParams) (*semanticapi.SemanticTokensDelta, error) {
	return nil, nil
}
func (noopLSP) PrepareTypeHierarchy(context.Context, semanticapi.TypeHierarchyPrepareParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) TypeHierarchySupertypes(context.Context, semanticapi.TypeHierarchySupertypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) TypeHierarchySubtypes(context.Context, semanticapi.TypeHierarchySubtypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) InlayHint(context.Context, semanticapi.InlayHintParams) ([]semanticapi.InlayHint, error) {
	return nil, nil
}
func (noopLSP) InlayHintResolve(context.Context, semanticapi.InlayHint) (semanticapi.InlayHint, error) {
	return semanticapi.InlayHint{}, nil
}
func (noopLSP) InlineValue(context.Context, semanticapi.InlineValueParams) ([]semanticapi.InlineValue, error) {
	return nil, nil
}
func (noopLSP) WillCreateFiles(context.Context, semanticapi.CreateFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillRenameFiles(context.Context, semanticapi.RenameFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillDeleteFiles(context.Context, semanticapi.DeleteFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillSave(context.Context, semanticapi.WillSaveTextDocumentParams) error {
	return nil
}
func (noopLSP) DidChangeConfiguration(context.Context, semanticapi.DidChangeConfigurationParams) error {
	return nil
}
func (noopLSP) DidChangeWatchedFiles(context.Context, semanticapi.DidChangeWatchedFilesParams) error {
	return nil
}
func (noopLSP) DidChangeWorkspaceFolders(context.Context, semanticapi.DidChangeWorkspaceFoldersParams) error {
	return nil
}
func (noopLSP) WorkDoneProgressCancel(context.Context, semanticapi.WorkDoneProgressCancelParams) error {
	return nil
}
func (noopLSP) SetTrace(context.Context, semanticapi.SetTraceParams) error { return nil }
func (noopLSP) DidCreateFiles(context.Context, semanticapi.CreateFilesParams) error {
	return nil
}
func (noopLSP) DidRenameFiles(context.Context, semanticapi.RenameFilesParams) error {
	return nil
}
func (noopLSP) DidDeleteFiles(context.Context, semanticapi.DeleteFilesParams) error {
	return nil
}

var _ semanticapi.LSP = noopLSP{}

// noopNotifications discards every notification. dream only uses
// Notifications to warn about skill registry issues; we never load
// skills in this test.
type noopNotifications struct{}

func (noopNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (noopNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (noopNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

var (
	_ browserapi.Notifications = noopNotifications{}
)
