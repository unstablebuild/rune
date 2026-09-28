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

package dream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/gitenv"
)

// ProgressType describes the kind of progress being reported.
type ProgressType int

const (
	// ProgressBootstrap reports initialization of the memory workspace.
	ProgressBootstrap ProgressType = iota + 1 // Initializing memory workspace
	// ProgressAnalyzing reports analysis of a conversation.
	ProgressAnalyzing // Analyzing a conversation
	// ProgressWriting reports writing memory files.
	ProgressWriting // Writing memory files
	// ProgressVerifying reports verification steps such as go build/test.
	ProgressVerifying // Running go build/test
	// ProgressFixing reports automated fix attempts.
	ProgressFixing // Re-running agent to fix failed tests
	// ProgressMigrating reports migration after schema upgrades.
	ProgressMigrating // Fixing compilation after schema upgrade
	// ProgressReprocessing reports re-dreaming old dialogues after schema changes.
	ProgressReprocessing // Re-dreaming old dialogues for new schema
	// ProgressToolCall reports an agent tool invocation.
	ProgressToolCall // Agent invoked a tool
	// ProgressToolResult reports completion of an agent tool.
	ProgressToolResult // Tool finished executing
	// ProgressError reports a non-fatal error while processing a dialogue.
	ProgressError // Non-fatal error processing a dialogue
	// ProgressPhaseStart reports the start of a post-dream phase.
	ProgressPhaseStart // Starting a post-dream phase
	// ProgressPhaseFinish reports successful completion of a phase.
	ProgressPhaseFinish // Phase completed successfully
	// ProgressDone reports completion of all dialogue processing.
	ProgressDone // Finished all dialogues
)

// Progress reports forward movement of the dream process.
type Progress struct {
	Type       ProgressType
	DialogueID string
	Message    string
	Progress   int    // current item index (0-based)
	Total      int    // total items to process
	Units      string // e.g., "dialogues"
	ToolName   string // for ProgressToolCall/ProgressToolResult
	IsError    bool   // for ProgressToolResult: was it a tool error?
	// Duration is set on ProgressToolResult and reports how long the
	// tool took to execute. Zero when the tool did not report a
	// duration.
	Duration time.Duration
}

// Deps holds all dependencies for the Dream function.
type Deps struct {
	LLM            llmapi.Service
	Store          dialoguemanager.Store // conversation source
	Storage        storageapi.Service    // dream state persistence
	FS             workspaceapi.FileSystem
	Exec           workspaceapi.Executor
	LSP            semanticapi.LSP          // for querying memory workspace categories
	Parser         syntaxapi.Parser         // for structural queries
	Notifications  browserapi.Notifications // for skill registry warnings
	DataPath       string                   // memory workspace root
	Model          llmapi.ModelEntry        // resolved LLM model entry (passed to agent config)
	MaxFixAttempts int                      // 0 uses defaultMaxFixAttempts

	// SourceDialoguePrompt, if set, replaces the default "Source Dialogue
	// Tracking" section in the system prompt. Use this to describe the
	// conversation source and which helper function to call in
	// FetchConversation implementations.
	SourceDialoguePrompt string

	// FetchHelper is the name of the helper function that FetchConversation
	// implementations should call (e.g. "FetchDialogue", "FetchClaudeDialogue").
	// When set, all examples in the system prompt use this name instead of
	// "FetchDialogue". Defaults to "FetchDialogue" when empty.
	FetchHelper string
}

const (
	dreamMaxIterations    = 50
	defaultMaxFixAttempts = 3
)

// Phase represents a processing phase in the dream pipeline.
// Extract is Phase 1; future quality/validation phases follow.
type Phase struct {
	Name        string
	Description string
	Run         func(ctx context.Context, ch chan<- Progress, deps Deps,
		state *DreamState, stateStore *dreamStateStore) error
}

// phases holds the registered pipeline phases in execution order.
// Future issues will append to this slice (deduplicate, consolidate, etc.).
var phases = []Phase{
	{
		Name:        "extract",
		Description: "Extract memories from conversations",
		Run:         runExtractPhase,
	},
	// refine runs after extract and uses the dialogues already walked
	// in this epoch as evidence for improving the recall machinery
	// (scorer, predicates, Scope, RecallInput).
	NewAgentPhase(
		"refine",
		"Refine recall machinery",
		refineSystemPrompt(),
		buildRefineUserPrompt,
	),
}

// NewAgentPhase creates a Phase that runs a single agent with the given
// system prompt and user prompt. If userPrompt returns "", the phase is
// skipped. After the agent runs, the phase verifies and retries on failure.
func NewAgentPhase(name, description, systemPrompt string,
	userPrompt func(ctx context.Context, deps Deps) (string, error)) Phase {
	return Phase{
		Name:        name,
		Description: description,
		Run: func(ctx context.Context, ch chan<- Progress, deps Deps,
			state *DreamState, stateStore *dreamStateStore) error {
			msg, err := userPrompt(ctx, deps)
			if err != nil {
				return fmt.Errorf("user prompt: %w", err)
			}
			if msg == "" {
				return nil
			}

			cwd, err := deps.FS.URI(deps.DataPath)
			if err != nil {
				return fmt.Errorf("resolve cwd URI: %w", err)
			}
			tools, _ := agentools.DefaultTools(deps.FS, deps.Exec, cwd, deps.LSP, agentools.Config{}, configedit.NopConfig())
			registry := agent.NewRegistry(tools...)
			skillRegistry := skills.NewRegistry(deps.FS, cwd, nil, deps.Notifications)
			store := newEphemeralStore()

			ag := agent.NewAgent(deps.LLM, registry, skillRegistry, store, agent.NoMemory(), agent.Config{
				MaxIterations: dreamMaxIterations,
				SystemPrompt:  systemPrompt,
				Model:         deps.Model,
			})

			dialogueID := uuid.New().String()

			if err := runAgent(ctx, ch, ag, dialogueID, "", msg); err != nil {
				return fmt.Errorf("agent: %w", err)
			}

			maxFix := deps.MaxFixAttempts
			if maxFix == 0 {
				maxFix = defaultMaxFixAttempts
			}

			for attempt := range maxFix {
				verifyErr := verify(ctx, deps.Exec, deps.DataPath)
				if verifyErr == nil {
					return nil
				}

				slog.Warn("phase verify failed, running agent to fix",
					"struct", "dream", "phase", name,
					"attempt", attempt+1, "maxAttempts", maxFix,
					"error", verifyErr)

				fixPrompt := fmt.Sprintf("`go test ./...` failed.\n"+
					"Fix the code so the tests pass.\n\n"+
					"```\n%s\n```", verifyErr)
				if err := runAgent(ctx, ch, ag, dialogueID, "", fixPrompt); err != nil {
					return fmt.Errorf("fix agent (attempt %d/%d): %w", attempt+1, maxFix, err)
				}
			}

			if err := verify(ctx, deps.Exec, deps.DataPath); err != nil {
				return fmt.Errorf("verify after %d fix attempts: %w", maxFix, err)
			}
			return nil
		},
	}
}

// Dream processes unprocessed dialogues and extracts durable memories.
// It returns an iterator that yields Progress events. Errors terminate
// the iterator: Next returns false and Err returns the error.
// Per-dialogue agent failures are non-fatal: the dialogue is skipped
// and retried on the next Dream call.
func Dream(ctx context.Context, deps Deps) (iterator.Iterator[Progress], error) {
	if err := validateDeps(deps); err != nil {
		return nil, err
	}

	ch := make(chan Progress, 1)
	runCtx, cancel := context.WithCancel(ctx)
	it := &progressIterator{
		ch:     ch,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go debug.CapturePanicReport(func() {

		defer close(it.done)
		defer close(ch)
		it.runErr = runDream(runCtx, ch, deps)

	})

	return it, nil
}

func validateDeps(deps Deps) error {
	switch {
	case deps.LLM == nil:
		panic("dream: LLM is required")
	case deps.Store == nil:
		panic("dream: Store is required")
	case deps.Storage == nil:
		panic("dream: Storage is required")
	case deps.FS == nil:
		panic("dream: FS is required")
	case deps.Exec == nil:
		panic("dream: Exec is required")
	case deps.LSP == nil:
		panic("dream: LSP is required")
	case deps.DataPath == "":
		panic("dream: DataPath is required")
	}
	return nil
}

func runDream(ctx context.Context, ch chan<- Progress, deps Deps) error {
	// Bootstrap.
	emit(ctx, ch, Progress{Type: ProgressBootstrap, Message: "initializing memory workspace"})
	bsResult, err := bootstrap(deps.FS, deps.DataPath)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	switch bsResult.Action {
	case actionCreated:
		emit(ctx, ch, Progress{Type: ProgressBootstrap, Message: "bootstrapped memory module"})
	case actionUpgraded:
		emit(ctx, ch, Progress{Type: ProgressBootstrap, Message: "upgraded memory module"})
	}

	// Run go mod tidy after fresh bootstrap or upgrade.
	if bsResult.Action == actionCreated || bsResult.Action == actionUpgraded {
		emit(ctx, ch, Progress{Type: ProgressBootstrap,
			Message: "upgrading memory module dependencies"})
		if err := goModTidy(ctx, deps.Exec, deps.DataPath); err != nil {
			return fmt.Errorf("go mod tidy: %w", err)
		}
		emit(ctx, ch, Progress{Type: ProgressBootstrap,
			Message: "upgraded memory module dependencies"})
	}

	// If upgraded, fix existing memories for the new template version.
	if bsResult.Action == actionUpgraded {
		if err := fixUpgradeCompat(ctx, ch, deps, bsResult.FromVersion); err != nil {
			return fmt.Errorf("fix upgrade: %w", err)
		}
	}

	// Ensure memory workspace is a git repository when git is available.
	// Git is required for the validation phase's worktree fork but the
	// pipeline degrades gracefully without it.
	hasGit := gitAvailable()
	if hasGit {
		if err := ensureGitRepo(ctx, deps.FS, deps.Exec, deps.DataPath); err != nil {
			return fmt.Errorf("ensure git repo: %w", err)
		}

		// If bootstrap created or upgraded, commit clean state.
		if bsResult.Action == actionCreated || bsResult.Action == actionUpgraded {
			if err := gitCommitAll(ctx, deps.Exec, deps.DataPath, "bootstrap/upgrade memory module"); err != nil {
				return fmt.Errorf("git commit bootstrap: %w", err)
			}
		}
	}

	// Load dream state.
	stateStore := newDreamState(deps.Storage)
	state, err := stateStore.load(ctx)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Run pipeline phases.
	for _, phase := range phases {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Skip non-extract phases that have already processed the current
		// extraction epoch, or when no extraction has produced memories yet.
		if phase.Name != "extract" {
			if state.LastExtract == 0 || state.PhasesRun[phase.Name] >= state.LastExtract {
				continue
			}
		}

		emit(ctx, ch, Progress{
			Type:    ProgressPhaseStart,
			Message: fmt.Sprintf("Starting phase: %s", phase.Description),
		})

		if err := phase.Run(ctx, ch, deps, &state, stateStore); err != nil {
			return fmt.Errorf("phase %s: %w", phase.Name, err)
		}

		// Track when non-extract phases last ran.
		if phase.Name != "extract" {
			state.PhasesRun[phase.Name] = state.LastExtract
			if err := stateStore.save(ctx, state); err != nil {
				return fmt.Errorf("save state after phase %s: %w", phase.Name, err)
			}
		}

		emit(ctx, ch, Progress{
			Type:    ProgressPhaseFinish,
			Message: fmt.Sprintf("Completed phase: %s", phase.Description),
		})
	}

	// Commit all phase changes in a single commit. When the validation
	// phase is implemented, this commit only happens after validation
	// succeeds — keeping the git history clean.
	if hasGit {
		if err := gitCommitAll(ctx, deps.Exec, deps.DataPath, "dream: update memories"); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}
	}

	emit(ctx, ch, Progress{
		Type:    ProgressDone,
		Message: "Dream complete",
	})
	return nil
}

// runExtractPhase implements Phase 1: process dialogues into memories.
func runExtractPhase(ctx context.Context, ch chan<- Progress, deps Deps,
	state *DreamState, stateStore *dreamStateStore) error {
	log := slog.With("struct", "dream")

	// Discover existing categories via LSP.
	existingCategories := discoverCategories(ctx, deps.LSP)
	log.Debug("discovered categories", "count", len(existingCategories))

	// Reprocessing phase: IDs come from state.Dreamed (bounded, already in memory).
	needsReprocess := state.SchemaVersion < templateVersion
	var reprocessIDs []string
	reprocessSet := make(map[string]bool)
	if needsReprocess {
		for id := range state.Dreamed {
			reprocessIDs = append(reprocessIDs, id)
			reprocessSet[id] = true
		}
		sort.Strings(reprocessIDs)
	}
	reprocessTotal := len(reprocessIDs)

	var reprocessed, extracted int
	for idx, id := range reprocessIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, err := deps.Store.Get(ctx, id)
		if errors.Is(err, storageapi.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("get dialogue %s: %w", id, err)
		}
		emit(ctx, ch, Progress{
			Type:       ProgressReprocessing,
			DialogueID: d.ID,
			Message:    fmt.Sprintf("Re-processing conversation %q for schema upgrade", d.ID),
			Progress:   idx,
			Total:      reprocessTotal,
			Units:      "conversations",
		})
		if rpErr := dreamDialogue(ctx, ch, deps, d, existingCategories, idx, reprocessTotal); rpErr != nil {
			log.Warn("re-processing dialogue failed, skipping",
				"dialogueID", d.ID, "error", rpErr)
			emit(ctx, ch, Progress{
				Type:       ProgressError,
				DialogueID: d.ID,
				Message:    fmt.Sprintf("Failed to re-process conversation %q: %s", d.ID, rpErr),
				Progress:   idx,
				Total:      reprocessTotal,
				Units:      "conversations",
			})
			reprocessed++
			continue
		}
		state.Dreamed[d.ID] = d.Version
		if err := stateStore.save(ctx, *state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		reprocessed++
		extracted++
	}

	if needsReprocess {
		state.SchemaVersion = templateVersion
		if err := stateStore.save(ctx, *state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	// Safety net: ensure SchemaVersion is current even if re-processing
	// did not run (e.g. no previously-dreamed dialogues).
	if state.SchemaVersion < templateVersion {
		state.SchemaVersion = templateVersion
		if err := stateStore.save(ctx, *state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	// Pre-collect new/updated dialogue headers so callers (and the
	// REPL ProgressWriter) know the total work up front. Without this
	// total, percentage-style progress bars cannot render meaningful
	// numbers for the new-dialogue branch.
	newHeaders, err := collectNewHeaders(ctx, deps.Store, state, reprocessSet)
	if err != nil {
		return fmt.Errorf("list dialogues: %w", err)
	}
	newTotal := len(newHeaders)

	for idx, h := range newHeaders {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, err := deps.Store.Get(ctx, h.ID)
		if err != nil {
			log.Warn("get dialogue failed, skipping",
				"dialogueID", h.ID, "error", err)
			continue
		}
		emit(ctx, ch, Progress{
			Type:       ProgressAnalyzing,
			DialogueID: d.ID,
			Message:    fmt.Sprintf("conversation %q", d.ID),
			Progress:   idx,
			Total:      newTotal,
			Units:      "conversations",
		})
		if err := dreamDialogue(ctx, ch, deps, d, existingCategories, idx, newTotal); err != nil {
			log.Warn("dream dialogue failed, skipping",
				"dialogueID", d.ID, "error", err)
			emit(ctx, ch, Progress{
				Type:       ProgressError,
				DialogueID: d.ID,
				Message:    fmt.Sprintf("failed: %s", err),
				Progress:   idx,
				Total:      newTotal,
				Units:      "conversations",
			})
			continue
		}
		state.Dreamed[d.ID] = h.Version
		if err := stateStore.save(ctx, *state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		extracted++
	}

	// Increment LastExtract only when extraction actually produced new memories.
	// Failed attempts are not counted so downstream phases are not triggered
	// until real content exists.
	if extracted > 0 {
		state.LastExtract++
		if err := stateStore.save(ctx, *state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	return nil
}

// collectNewHeaders enumerates dialogue headers via deps.Store.List and
// returns those that need (re-)dreaming because they have not been
// processed for their current version. Reprocessing IDs are filtered
// out so they are not processed twice. The full slice is buffered in
// memory so callers know the total up front; the dialogue store is
// expected to contain at most thousands of headers.
func collectNewHeaders(
	ctx context.Context, store dialoguemanager.Store,
	state *DreamState, reprocessSet map[string]bool,
) ([]dialoguemanager.DialogueHeader, error) {
	it, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	filtered := iterator.Filter(it, func(h dialoguemanager.DialogueHeader) bool {
		if reprocessSet[h.ID] {
			return false
		}
		dreamedVersion, ok := state.Dreamed[h.ID]
		return !ok || h.Version > dreamedVersion
	})
	return iterator.ToSlice(ctx, filtered)
}

func dreamDialogue(
	ctx context.Context, ch chan<- Progress, deps Deps,
	d dialoguemanager.Dialogue, existingCategories []string,
	index, total int,
) error {
	transcript := formatTranscript(d)
	prompt := systemPrompt(deps.DataPath, existingCategories, deps.SourceDialoguePrompt, deps.FetchHelper)

	cwd, err := deps.FS.URI(deps.DataPath)
	if err != nil {
		return fmt.Errorf("resolve cwd URI: %w", err)
	}
	tools, _ := agentools.DefaultTools(deps.FS, deps.Exec, cwd, deps.LSP, agentools.Config{}, configedit.NopConfig())
	registry := agent.NewRegistry(tools...)
	skillRegistry := skills.NewRegistry(deps.FS, cwd, nil, deps.Notifications)
	store := newEphemeralStore()

	ag := agent.NewAgent(deps.LLM, registry, skillRegistry, store, agent.NoMemory(), agent.Config{
		MaxIterations: dreamMaxIterations,
		SystemPrompt:  prompt,
		Model:         deps.Model,
	})

	dialogueID := uuid.New().String()

	emit(ctx, ch, Progress{
		Type:       ProgressWriting,
		DialogueID: d.ID,
		Message:    "extracting memories",
		Progress:   index,
		Total:      total,
		Units:      "conversations",
	})

	preReads := preReadWorkspaceFiles(deps.FS, deps.DataPath)

	err = runAgent(ctx, ch, ag, dialogueID, d.ID,
		fmt.Sprintf("Source dialogue ID: %s\n\nAnalyze this conversation and extract durable memories:\n\n%s", d.ID, transcript),
		agent.WithToolCallResults(preReads),
	)
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	maxFix := deps.MaxFixAttempts
	if maxFix == 0 {
		maxFix = defaultMaxFixAttempts
	}

	for attempt := range maxFix {
		if err := ctx.Err(); err != nil {
			return err
		}
		emit(ctx, ch, Progress{
			Type:       ProgressVerifying,
			DialogueID: d.ID,
			Message:    "verifying memories",
			Progress:   index,
			Total:      total,
			Units:      "conversations",
		})

		verifyErr := verify(ctx, deps.Exec, deps.DataPath)
		if verifyErr == nil {
			return nil
		}

		slog.Warn("verification failed, running agent to fix",
			"struct", "dream", "dialogueID", d.ID,
			"attempt", attempt+1, "maxAttempts", maxFix,
			"error", verifyErr)

		emit(ctx, ch, Progress{
			Type:       ProgressFixing,
			DialogueID: d.ID,
			Message:    fmt.Sprintf("fixing tests (attempt %d/%d)", attempt+1, maxFix),
			Progress:   index,
			Total:      total,
			Units:      "conversations",
		})

		fixPrompt := fmt.Sprintf("`go test ./...` failed in the memory workspace.\n"+
			"Fix the code so the tests pass.\n\n"+
			"```\n%s\n```", verifyErr)
		if err := runAgent(ctx, ch, ag, dialogueID, d.ID, fixPrompt); err != nil {
			return fmt.Errorf("fix agent (attempt %d/%d): %w", attempt+1, maxFix, err)
		}
	}

	// Final verify after last fix attempt.
	emit(ctx, ch, Progress{
		Type:       ProgressVerifying,
		DialogueID: d.ID,
		Message:    "final verification",
		Progress:   index,
		Total:      total,
		Units:      "conversations",
	})
	if err := verify(ctx, deps.Exec, deps.DataPath); err != nil {
		return fmt.Errorf("verify after %d fix attempts: %w", maxFix, err)
	}
	return nil
}

// runAgent executes one agent turn, forwarding tool call and result
// events as Progress so the caller can display them.
func runAgent(
	ctx context.Context, ch chan<- Progress,
	ag *agent.Agent, dialogueID, sourceDialogueID string,
	message string, opts ...agent.RunOption,
) error {
	it := ag.Run(ctx, dialogueID, message, opts...)

	var agentErr error
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		switch ev.Type {
		case agent.EventToolCall:
			emit(ctx, ch, Progress{
				Type:       ProgressToolCall,
				DialogueID: sourceDialogueID,
				ToolName:   ev.ToolName,
				Message:    ev.ToolSummary,
			})
		case agent.EventToolResult:
			emit(ctx, ch, Progress{
				Type:       ProgressToolResult,
				DialogueID: sourceDialogueID,
				ToolName:   ev.ToolName,
				Message:    ev.ToolSummary,
				IsError:    ev.IsError,
				Duration:   ev.ToolDuration,
			})
		case agent.EventError:
			agentErr = ev.Error
		}
	}
	if err := it.Err(); err != nil && agentErr == nil {
		agentErr = err
	}
	_ = it.Close()
	return agentErr
}

const goModTidyTimeout = 120 * time.Second

// goModTidy runs `go mod tidy` in the memory workspace to resolve dependencies.
func goModTidy(ctx context.Context, exec workspaceapi.Executor, dataPath string) error {
	ctx, cancel := context.WithTimeout(ctx, goModTidyTimeout)
	defer cancel()

	var buf bytes.Buffer
	watcher := workspaceapi.ChanProcessWatcher(make(chan error, 1))

	_, err := exec.Start(ctx, workspaceapi.Cmd{
		Path:    "go",
		Args:    []string{"mod", "tidy"},
		Dir:     dataPath,
		Env:     os.Environ(),
		Stdout:  &buf,
		Stderr:  &buf,
		Watcher: watcher,
	})
	if err != nil {
		return fmt.Errorf("start go mod tidy: %w", err)
	}

	select {
	case procErr := <-watcher.WatchProcess():
		if procErr != nil {
			return fmt.Errorf("go mod tidy failed:\n%s\n%w", buf.String(), procErr)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

const gitTimeout = 30 * time.Second

// gitAvailable reports whether the git binary is on the PATH.
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// ensureGitRepo initialises a git repository in dataPath if one does not
// already exist. This is required for the validation phase's git worktree fork.
func ensureGitRepo(ctx context.Context, fs workspaceapi.FileSystem,
	exec workspaceapi.Executor, dataPath string) error {

	_, err := fs.Stat(filepath.Join(dataPath, ".git"))
	if err == nil {
		return nil // already a git repo
	}

	if err := runGitCmd(ctx, exec, dataPath, "init"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := runGitCmd(ctx, exec, dataPath, "add", "."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := runGitCmd(ctx, exec, dataPath,
		"-c", "user.name=rune-agent", "-c", "user.email=rune-agent@localhost",
		"-c", "commit.gpgsign=false",
		"commit", "-m", "initialize memory module"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// gitCommitAll stages all changes and commits them. Returns nil if there
// are no changes to commit.
func gitCommitAll(ctx context.Context, exec workspaceapi.Executor,
	dataPath, message string) error {

	if err := runGitCmd(ctx, exec, dataPath, "add", "."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	err := runGitCmd(ctx, exec, dataPath,
		"-c", "user.name=rune-agent", "-c", "user.email=rune-agent@localhost",
		"-c", "commit.gpgsign=false",
		"commit", "-m", message)
	// git exits non-zero when there is nothing to commit; this is expected
	// and not an error for our use case.
	if err != nil && strings.Contains(err.Error(), "nothing to commit") {
		return nil
	}
	return err
}

// runGitCmd runs a git command in the given directory and waits for it to finish.
func runGitCmd(ctx context.Context, exec workspaceapi.Executor,
	dataPath string, args ...string) error {

	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	var buf bytes.Buffer
	watcher := workspaceapi.ChanProcessWatcher(make(chan error, 1))

	_, err := exec.Start(ctx, workspaceapi.Cmd{
		Path:    "git",
		Args:    args,
		Dir:     dataPath,
		Env:     gitenv.Environ(),
		Stdout:  &buf,
		Stderr:  &buf,
		Watcher: watcher,
	})
	if err != nil {
		return fmt.Errorf("start git %s: %w", args[0], err)
	}

	select {
	case procErr := <-watcher.WatchProcess():
		if procErr != nil {
			return fmt.Errorf("git %s failed:\n%s\n%w", args[0], buf.String(), procErr)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// fixUpgradeCompat runs versioned migration agents to fix compilation errors
// introduced by template upgrades between fromVersion and templateVersion.
func fixUpgradeCompat(ctx context.Context, ch chan<- Progress, deps Deps, fromVersion int) error {
	if fromVersion < 1 {
		fromVersion = 1
	}

	emit(ctx, ch, Progress{Type: ProgressMigrating,
		Message: "verifying memory module upgrade"})
	buildErr := verify(ctx, deps.Exec, deps.DataPath)
	if buildErr == nil {
		emit(ctx, ch, Progress{Type: ProgressMigrating,
			Message: "verified memory module upgrade"})
		return nil
	}

	for v := fromVersion + 1; v <= templateVersion; v++ {
		m := migrations[v]

		emit(ctx, ch, Progress{
			Type:    ProgressMigrating,
			Message: fmt.Sprintf("migrating v%d\u2192v%d: %s", m.From, m.To, m.Description),
		})

		cwd, err := deps.FS.URI(deps.DataPath)
		if err != nil {
			return fmt.Errorf("resolve cwd URI: %w", err)
		}
		tools, _ := agentools.DefaultTools(deps.FS, deps.Exec, cwd, deps.LSP, agentools.Config{}, configedit.NopConfig())
		reg := agent.NewRegistry(tools...)
		skillReg := skills.NewRegistry(deps.FS, cwd, nil, deps.Notifications)
		store := newEphemeralStore()

		ag := agent.NewAgent(deps.LLM, reg, skillReg, store, agent.NoMemory(), agent.Config{
			MaxIterations: dreamMaxIterations,
			SystemPrompt:  m.FixPrompt,
			Model:         deps.Model,
		})

		dialogueID := uuid.New().String()
		msg := fmt.Sprintf("`go test ./...` failed after template upgrade.\n"+
			"Fix all memory files so they compile and tests pass.\n\n"+
			"```\n%s\n```", buildErr)

		if err := runAgent(ctx, ch, ag, dialogueID, "", msg); err != nil {
			return fmt.Errorf("fix agent v%d\u2192v%d: %w", m.From, m.To, err)
		}

		buildErr = verify(ctx, deps.Exec, deps.DataPath)
		if buildErr != nil {
			return fmt.Errorf("verify after migration v%d\u2192v%d: %w", m.From, m.To, buildErr)
		}
	}

	return nil
}

const verifyTimeout = 120 * time.Second

// verify runs `go test ./...` in the memory workspace to confirm
// the generated code compiles and the bootstrapped tests pass.
func verify(ctx context.Context, exec workspaceapi.Executor, dataPath string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	var buf bytes.Buffer
	watcher := workspaceapi.ChanProcessWatcher(make(chan error, 1))

	_, err := exec.Start(ctx, workspaceapi.Cmd{
		Path:    "go",
		Args:    []string{"test", "./..."},
		Dir:     dataPath,
		Env:     os.Environ(),
		Stdout:  &buf,
		Stderr:  &buf,
		Watcher: watcher,
	})
	if err != nil {
		return fmt.Errorf("start go test: %w", err)
	}

	select {
	case procErr := <-watcher.WatchProcess():
		if procErr != nil {
			return fmt.Errorf("go test failed:\n%s\n%w", buf.String(), procErr)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// discoverCategories queries the LSP for interface symbols in the
// memory workspace. Returns nil if LSP is unavailable.
func discoverCategories(ctx context.Context, lsp semanticapi.LSP) []string {
	if lsp == nil {
		return nil
	}

	symbols, err := lsp.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
		Query: "Memory",
	})
	if err != nil {
		slog.Debug("failed to discover categories via LSP", "error", err)
		return nil
	}

	var categories []string
	for _, sym := range symbols {
		if sym.Kind == semanticapi.SymbolKindInterface {
			categories = append(categories, sym.Name)
		}
	}
	return categories
}

// workspaceFiles are the files every dream agent reads at the start.
// Pre-reading them avoids redundant read_file tool calls.
var workspaceFiles = []string{
	"categories.go",
	"claude.go",
	"conversation.go",
	"main.go",
	"main_test.go",
}

// preReadWorkspaceFiles reads the standard workspace files from disk
// and returns them as ToolCallResult entries for read_file.
func preReadWorkspaceFiles(fs workspaceapi.FileSystem, dataPath string) []agent.ToolCallResult {
	var results []agent.ToolCallResult
	for _, name := range workspaceFiles {
		path := filepath.Join(dataPath, name)
		f, err := fs.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			continue
		}
		results = append(results, agent.ToolCallResult{
			ToolName:  "read_file",
			Arguments: fmt.Sprintf(`{"path":%q}`, name),
			Content:   string(data),
		})
	}
	return results
}

// formatTranscript formats a dialogue into a readable transcript,
// skipping system role messages.
func formatTranscript(d dialoguemanager.Dialogue) string {
	var b strings.Builder
	for _, msg := range d.Messages {
		if msg.Role == llmapi.RoleSystem {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", msg.Role, msg.Content)
	}
	return b.String()
}

func emit(ctx context.Context, ch chan<- Progress, p Progress) {
	select {
	case ch <- p:
	case <-ctx.Done():
	}
}

// progressIterator adapts a channel to iterator.Iterator[Progress].
// The runErr field is set by the producer goroutine before closing ch.
type progressIterator struct {
	ch     <-chan Progress
	runErr error // set by goroutine before close(ch)
	mu     sync.Mutex
	err    error
	// cancel cancels the producer's derived context. Calling Close
	// invokes it; subsequent calls are no-ops thanks to sync.Once.
	cancel     context.CancelFunc
	cancelOnce sync.Once
	// done is closed by the producer goroutine when it exits.
	done chan struct{}
}

func (p *progressIterator) Next(ctx context.Context) (Progress, bool) {
	select {
	case ev, ok := <-p.ch:
		if !ok {
			p.mu.Lock()
			if p.runErr != nil {
				p.err = p.runErr
			}
			p.mu.Unlock()
			return Progress{}, false
		}
		return ev, true
	case <-ctx.Done():
		p.mu.Lock()
		p.err = ctx.Err()
		p.mu.Unlock()
		return Progress{}, false
	}
}

func (p *progressIterator) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *progressIterator) Close() error {
	if p.cancel != nil {
		p.cancelOnce.Do(p.cancel)
	}
	if p.done != nil {
		// Wait for the producer goroutine to fully exit so callers can
		// rely on Close meaning "producer stopped". Use a generous
		// safety deadline to avoid hanging the REPL if the producer is
		// stuck in a system call that ignores ctx cancellation.
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
		}
	}
	// Surface the producer's terminal error (typically ctx.Canceled
	// after Close) so callers querying Err() after Close see it even if
	// they never drained the channel to completion via Next.
	p.mu.Lock()
	if p.err == nil && p.runErr != nil {
		p.err = p.runErr
	}
	p.mu.Unlock()
	return nil
}
