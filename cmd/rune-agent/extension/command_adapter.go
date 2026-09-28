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
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/audit"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agentshell"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
	"unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"
)

// commandAdapter wraps a repl.CommandHandler into a dialoguetui.CommandHandler
// with session-aware overrides for commands like /clear, /history, and /model.
type commandAdapter struct {
	handler    repl.CommandHandler
	dialogueID string
	resetFn    func() // visual reset (Component.Reset under lock)
	wm         browserapi.WindowManager
	store      dialoguemanager.Store
	// compactFn replaces messages in the component after a successful compact.
	compactFn func(msgs []llmapi.Message)

	// Model switching support. When agent is non-nil, the /model command
	// can switch the backing LLM service mid-conversation.
	agent        *agent.Agent
	llmSvc       llmapi.Service
	config       configedit.Config
	ctx          context.Context
	storage      storageapi.Service
	currentModel string
	auditStore   *audit.Store

	// Skill resolution. When a /name command matches a skill, the
	// formatted skill content is returned as UserMessage.
	skillRegistry *skills.SkillRegistry

	// Conversation patch review (/diff). editor opens the aggregate diff
	// in a floating window and attachFn hands the edited text back to the
	// live dialogue component as a pending attachment.
	editor      text.Editor
	editorModal bool
	parser      syntaxapi.Parser
	fs          workspaceapi.FileSystem
	cwd         workspaceapi.URI
	// git backs /reviewall. It is nil when no version control is wired.
	git vctrl.Service
	// reviewContext is how many workspace lines are shown around each
	// hunk (extensions.rune-agent.config.review_context_lines).
	reviewContext int
	attachFn      func(dialoguetui.Attachment)
	reviewSeq     int
	// statusBarFn mutates the chat status bar state under the component
	// lock. It is nil for sessions without a status bar.
	statusBarFn func(func(*dialoguetui.StatusBarState))
}

// commandAdapterDeps groups the values commandAdapter borrows from its
// owning aiEditorHandler. Keeping them in a struct lets handleChat thread
// a single argument to newCommandAdapter instead of a long parameter list.
type commandAdapterDeps struct {
	handler       repl.CommandHandler
	dialogueID    string
	wm            browserapi.WindowManager
	store         dialoguemanager.Store
	llmSvc        llmapi.Service
	config        configedit.Config
	ctx           context.Context
	storage       storageapi.Service
	currentModel  string
	skillRegistry *skills.SkillRegistry
	auditStore    *audit.Store
	editor        text.Editor
	editorModal   bool
	parser        syntaxapi.Parser
	fs            workspaceapi.FileSystem
	cwd           workspaceapi.URI
	git           vctrl.Service
	reviewContext int

	// mu guards comp writes; the closures below take it.
	mu          *sync.Mutex
	comp        **dialoguetui.Component // late-bound: caller assigns *comp later
	interrupter term.Interrupter
}

// newCommandAdapter builds the commandAdapter and its resetFn / compactFn
// closures. The closures dereference *deps.comp at call time, so the
// caller can construct the adapter before instantiating the component
// and assign *deps.comp afterwards.
func newCommandAdapter(deps commandAdapterDeps) *commandAdapter {
	return &commandAdapter{
		handler:       deps.handler,
		dialogueID:    deps.dialogueID,
		wm:            deps.wm,
		store:         deps.store,
		llmSvc:        deps.llmSvc,
		config:        deps.config,
		ctx:           deps.ctx,
		storage:       deps.storage,
		currentModel:  deps.currentModel,
		skillRegistry: deps.skillRegistry,
		auditStore:    deps.auditStore,
		editor:        deps.editor,
		editorModal:   deps.editorModal,
		parser:        deps.parser,
		fs:            deps.fs,
		cwd:           deps.cwd,
		git:           deps.git,
		reviewContext: deps.reviewContext,
		resetFn: func() {
			deps.mu.Lock()
			(*deps.comp).Reset()
			deps.mu.Unlock()
		},
		attachFn: func(a dialoguetui.Attachment) {
			deps.mu.Lock()
			(*deps.comp).UpsertAttachment(a)
			deps.mu.Unlock()
		},
		statusBarFn: func(fn func(*dialoguetui.StatusBarState)) {
			deps.mu.Lock()
			(*deps.comp).SetStatusBarState(fn)
			deps.mu.Unlock()
		},
		compactFn: func(msgs []llmapi.Message) {
			deps.mu.Lock()
			comp := *deps.comp
			comp.Reset()
			pending := make(map[string]llmapi.ToolCall)
			for _, msg := range msgs {
				addMessage(comp, msg, pending)
			}
			deps.mu.Unlock()
			_ = deps.interrupter.Interrupt(context.Background())
		},
	}
}

func (a *commandAdapter) HandleCommand(
	ctx context.Context, name string, args []string,
) (dialoguetui.CommandResult, error) {
	// Check if the command matches a skill before dispatching as a shell command.
	if skill, ok := a.skillRegistry.Get(name); ok {
		return dialoguetui.CommandResult{
			UserMessage: formatSkillMessage(skill, strings.Join(args, " ")),
			SkillName:   skill.Name,
		}, nil
	}

	switch name {
	case "clear":
		if err := rejectPositionalID(name, args); err != nil {
			return dialoguetui.CommandResult{}, err
		}
		return a.handleClear(ctx)
	case "history":
		if err := rejectPositionalID(name, args); err != nil {
			return dialoguetui.CommandResult{}, err
		}
		name = "chats"
		args = []string{"show", a.dialogueID}
	case "compact":
		if len(args) > 1 {
			return dialoguetui.CommandResult{}, errors.New("usage: /compact [<model>]")
		}
		return a.handleCompact(ctx, args)
	case "export":
		if err := rejectPositionalID(name, args); err != nil {
			return dialoguetui.CommandResult{}, err
		}
		name = "chats"
		args = append([]string{"export"}, args...)
		args = append(args, a.dialogueID)
	case "log":
		if err := rejectPositionalID(name, args); err != nil {
			return dialoguetui.CommandResult{}, err
		}
		name = "chats"
		args = []string{"log", a.dialogueID}
	case "model":
		return a.handleModel(ctx, args)
	case "effort":
		return a.handleEffort(args)
	case "max_tokens":
		return a.handleMaxTokens(args)
	case "fork":
		if err := rejectPositionalID(name, args); err != nil {
			return dialoguetui.CommandResult{}, err
		}
		name = "chats"
		args = []string{"fork", a.dialogueID}
	case "reviewchanges":
		if err := rejectPositionalID(name, args); err != nil {
			return dialoguetui.CommandResult{}, err
		}
		return a.handleReviewChanges(ctx)
	case "reviewall":
		if err := rejectPositionalID(name, args); err != nil {
			return dialoguetui.CommandResult{}, err
		}
		return a.handleReviewAll(ctx)
	}
	it, err := a.handler.HandleCommand(
		ctx, repl.Command{Name: name, Args: args}, repl.NopProgressWriter(),
	)
	if errors.Is(err, agentshell.ErrExit) {
		return dialoguetui.CommandResult{Exit: true}, nil
	}
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}

	items, drainErr := iterator.ToSlice(ctx, it)
	_ = it.Close()
	if drainErr != nil {
		return dialoguetui.CommandResult{}, drainErr
	}
	if len(items) == 0 {
		return dialoguetui.CommandResult{}, nil
	}

	a.openCommandFloating(items)
	return dialoguetui.CommandResult{}, nil
}

// rejectPositionalID returns an error if args contains a non-flag
// positional argument. Chat commands act on the open chat only, so a
// dialogue id is no longer accepted.
func rejectPositionalID(name string, args []string) error {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			return fmt.Errorf("/%s does not take a dialogue id; it acts on the open chat", name)
		}
	}
	return nil
}

// formatSkillMessage formats the user-visible portion of a slash-command
// invocation. The full skill body is injected as a system message by
// Agent.run via WithSkillName; this function only emits the
// command envelope and any user-supplied arguments.
//
// A <command-hint> envelope is appended so the model is told, inline
// in the user turn, to invoke the skill tool. Without this cue the
// model frequently fails to notice the system-message skill body and
// either ignores the command or falls back to request_skill.
func formatSkillMessage(skill skills.Skill, args string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<command-message>%s</command-message>\n", skill.Name)
	fmt.Fprintf(&b, "<command-name>/%s</command-name>", skill.Name)
	if args != "" {
		b.WriteString("\n")
		b.WriteString(args)
	}
	fmt.Fprintf(
		&b,
		"\n<command-hint>Call the skill tool with name=%q to load and run this skill.</command-hint>",
		skill.Name,
	)
	return b.String()
}

func parseStoredCommandMessage(content string) (string, bool) {
	const (
		messageOpen  = "<command-message>"
		messageClose = "</command-message>"
		nameOpen     = "<command-name>"
		nameClose    = "</command-name>"
		hintOpen     = "<command-hint>"
		hintClose    = "</command-hint>"
	)

	if !strings.HasPrefix(content, messageOpen) {
		return "", false
	}
	rest := strings.TrimPrefix(content, messageOpen)
	messageEnd := strings.Index(rest, messageClose)
	if messageEnd < 0 {
		return "", false
	}
	messageName := rest[:messageEnd]
	rest = rest[messageEnd+len(messageClose):]
	if !strings.HasPrefix(rest, "\n"+nameOpen) {
		return "", false
	}
	rest = strings.TrimPrefix(rest, "\n"+nameOpen)
	nameEnd := strings.Index(rest, nameClose)
	if nameEnd < 0 {
		return "", false
	}
	commandName := rest[:nameEnd]
	rest = rest[nameEnd+len(nameClose):]

	if messageName == "" || commandName == "" || !strings.HasPrefix(commandName, "/") {
		return "", false
	}
	if strings.TrimPrefix(commandName, "/") != messageName {
		return "", false
	}
	if hintIdx := strings.LastIndex(rest, "\n"+hintOpen); hintIdx >= 0 {
		if strings.HasSuffix(rest, hintClose) {
			rest = rest[:hintIdx]
		}
	}
	if rest == "" {
		return commandName, true
	}
	if !strings.HasPrefix(rest, "\n") {
		return "", false
	}
	args := strings.TrimPrefix(rest, "\n")
	if args == "" {
		return commandName, true
	}
	if strings.Contains(args, "\n") {
		return commandName + "\n" + args, true
	}
	return commandName + " " + args, true
}

// handleModel shows the current model or switches to a new one.
func (a *commandAdapter) handleModel(
	ctx context.Context, args []string,
) (dialoguetui.CommandResult, error) {
	if len(args) == 0 {
		md, err := markdown.New(a.resolvedModelLabel(ctx))
		if err != nil {
			return dialoguetui.CommandResult{}, err
		}
		return dialoguetui.CommandResult{
			Display: iterator.FromSlice([]component.Responsive{md}),
		}, nil
	}
	arg := args[0]
	entry, err := llmarg.Resolve(ctx, a.llmSvc, arg)
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	svc := a.llmSvc
	if a.auditStore != nil {
		svc = audit.NewService(svc, a.auditStore, entry)
	}
	a.agent.SwapService(svc, entry)
	a.syncStatusBarModel()
	qualified := entry.Provider + "/" + entry.Name
	a.currentModel = qualified
	md, err := markdown.New(fmt.Sprintf("Switched to model **%s**", qualified))
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	return dialoguetui.CommandResult{
		Display: iterator.FromSlice([]component.Responsive{md}),
	}, nil
}

// resolvedModelLabel returns the qualified provider/model the session
// currently uses. currentModel may be a router alias (e.g. "default"),
// so it is resolved through the service rather than displayed verbatim.
func (a *commandAdapter) resolvedModelLabel(ctx context.Context) string {
	if entry, err := llmarg.Resolve(ctx, a.llmSvc, a.currentModel); err == nil {
		return entry.Provider + "/" + entry.Name
	}
	return a.currentModel
}

// syncStatusBarModel republishes the agent's model, effort and token
// budgets to the chat status bar. Call it after anything that rebinds
// the session to a different model or changes those budgets.
func (a *commandAdapter) syncStatusBarModel() {
	if a.agent == nil || a.statusBarFn == nil {
		return
	}
	entry := a.agent.ModelEntry()
	effort := string(a.agent.Effort())
	if effort == "" {
		// Name the level the provider will actually apply rather than
		// leaving the bar reading "default".
		effort = string(llmarg.DefaultEffort(entry))
	}
	maxTokens := a.agent.MaxOutputTokens()
	a.statusBarFn(func(s *dialoguetui.StatusBarState) {
		s.Model = entry.Name
		s.Provider = entry.Provider
		s.Effort = effort
		s.MaxTokens = maxTokens
		if entry.ContextWindow > 0 {
			s.ContextWindow = entry.ContextWindow
		}
	})
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

// handleEffort shows the current effort level or sets a new one.
func (a *commandAdapter) handleEffort(args []string) (dialoguetui.CommandResult, error) {
	if len(args) == 0 {
		current := string(a.agent.Effort())
		if current == "" {
			current = "model default"
		}
		md, err := markdown.New(fmt.Sprintf("Current effort level: **%s**", current))
		if err != nil {
			return dialoguetui.CommandResult{}, err
		}
		return dialoguetui.CommandResult{
			Display: iterator.FromSlice([]component.Responsive{md}),
		}, nil
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
		return dialoguetui.CommandResult{}, fmt.Errorf(
			"invalid effort level %q: must be none, minimal, low, medium, high, xhigh, max, or ultra",
			args[0])
	}

	a.agent.SetEffort(level)
	a.syncStatusBarModel()
	md, err := markdown.New(fmt.Sprintf("Set effort level to **%s**.", level))
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	return dialoguetui.CommandResult{
		Display: iterator.FromSlice([]component.Responsive{md}),
	}, nil
}

// handleMaxTokens shows or sets the per-session max-output-token override.
func (a *commandAdapter) handleMaxTokens(args []string) (dialoguetui.CommandResult, error) {
	if len(args) == 0 {
		current := a.agent.MaxOutputTokens()
		text := "Current max output tokens: provider/config default"
		if current > 0 {
			text = fmt.Sprintf("Current max output tokens: **%d**", current)
		}
		md, err := markdown.New(text)
		if err != nil {
			return dialoguetui.CommandResult{}, err
		}
		return dialoguetui.CommandResult{
			Display: iterator.FromSlice([]component.Responsive{md}),
		}, nil
	}

	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		return dialoguetui.CommandResult{}, fmt.Errorf(
			"invalid max_tokens value %q: must be a positive integer", args[0])
	}

	if err := llmarg.ValidateMaxOutputTokens(a.agent.ModelEntry(), n); err != nil {
		return dialoguetui.CommandResult{}, err
	}

	a.agent.SetMaxOutputTokens(n)
	a.syncStatusBarModel()
	md, err := markdown.New(fmt.Sprintf("Set max output tokens to **%d**.", n))
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	return dialoguetui.CommandResult{
		Display: iterator.FromSlice([]component.Responsive{md}),
	}, nil
}

// handleClear resets the UI, runs the archive+clear via the shell, and
// returns the confirmation message inline instead of in a floating window.
func (a *commandAdapter) handleClear(ctx context.Context) (dialoguetui.CommandResult, error) {
	if a.resetFn != nil {
		a.resetFn()
	}
	it, err := a.handler.HandleCommand(ctx, repl.Command{
		Name: "chats", Args: []string{"clear", a.dialogueID},
	}, repl.NopProgressWriter())
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	items, drainErr := iterator.ToSlice(ctx, it)
	_ = it.Close()
	if drainErr != nil {
		return dialoguetui.CommandResult{}, drainErr
	}
	if len(items) == 0 {
		return dialoguetui.CommandResult{}, nil
	}
	return dialoguetui.CommandResult{
		Display: iterator.FromSlice(items),
	}, nil
}

// handleCompact returns a lazy iterator that blocks during the LLM
// summarisation call so AddCommand's animation plays. After the iterator
// is fully drained, its Close method (which runs AFTER AddCommand removes
// the animation node) resets the component and replays the compacted
// messages. This ordering avoids the panic from Remove-after-Reset.
func (a *commandAdapter) handleCompact(
	_ context.Context, args []string,
) (dialoguetui.CommandResult, error) {
	var model string
	if len(args) > 0 {
		model = args[0]
	}
	return dialoguetui.CommandResult{
		Phase: phaseCompacting,
		Display: &compactIterator{
			handler:    a.handler,
			store:      a.store,
			dialogueID: a.dialogueID,
			model:      model,
			compactFn:  a.compactFn,
		},
	}, nil
}

const (
	// chatReviewAttachmentIcon is the Nerd Font diff glyph. The strip
	// renders "<icon> <name>", so the leading space in the name widens
	// the gap to the two columns the label is specified with.
	chatReviewAttachmentIcon = '\uf4d2'
	chatReviewAttachmentID   = "chatreviewchanges"
	chatReviewAttachmentName = " changes review"
	chatReviewHeading        = "Changes review from the user:"

	chatReviewAllAttachmentID   = "chatreviewall"
	chatReviewAllAttachmentName = " working tree"
	chatReviewAllHeading        = "Working tree review from the user:"
)

// reviewAttachmentHeading introduces a review attachment to the model,
// which otherwise cannot tell a conversation review from a working tree
// one.
func reviewAttachmentHeading(id string) string {
	if id == chatReviewAllAttachmentID {
		return chatReviewAllHeading
	}
	return chatReviewHeading
}

// handleReviewChanges opens every apply_patch invocation of this
// conversation as a single editable diff in a centered floating window.
// Whatever the user leaves behind, if it differs from what was opened,
// becomes a pending attachment carried into the next message.
func (a *commandAdapter) handleReviewChanges(
	ctx context.Context,
) (dialoguetui.CommandResult, error) {
	if a.editor == nil || a.attachFn == nil {
		return dialoguetui.CommandResult{},
			errors.New("/reviewchanges requires a configured editor")
	}
	d, err := a.store.Get(ctx, a.dialogueID)
	if err != nil {
		return dialoguetui.CommandResult{},
			fmt.Errorf("get conversation %q: %w", a.dialogueID, err)
	}
	review, err := reviewChanges(d.Messages, workspaceView{
		src:     workspaceSource(a.fs, a.cwd),
		context: a.reviewContext,
	})
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	return a.openReview(ctx, review,
		chatReviewAttachmentID, chatReviewAttachmentName)
}

// handleReviewAll opens everything uncommitted in the workspace
// repository as one editable diff, in the same window /reviewchanges
// uses.
func (a *commandAdapter) handleReviewAll(
	ctx context.Context,
) (dialoguetui.CommandResult, error) {
	if a.editor == nil || a.attachFn == nil {
		return dialoguetui.CommandResult{},
			errors.New("/reviewall requires a configured editor")
	}
	if a.git == nil {
		return dialoguetui.CommandResult{},
			errors.New("/reviewall requires version control")
	}
	diffs, err := a.git.WorkingDiff(ctx, a.cwd, a.reviewContext)
	if err != nil {
		return dialoguetui.CommandResult{},
			fmt.Errorf("diff working tree: %w", err)
	}
	review, err := reviewWorkingTree(diffs)
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	return a.openReview(ctx, review,
		chatReviewAllAttachmentID, chatReviewAllAttachmentName)
}

// openReview renders a review into a centered floating editor and, on
// close, attaches whatever the user left behind when it differs from
// what was opened.
func (a *commandAdapter) openReview(
	ctx context.Context, review changesReview, attachID, attachName string,
) (dialoguetui.CommandResult, error) {
	// Each invocation opens its own resource: the editor indexes open
	// handlers by URI, and a reopened diff must not collide with one the
	// user has not closed yet.
	a.reviewSeq++
	uri, err := workspaceapi.ParseURI(fmt.Sprintf(
		"rune-agent-review://%s/%d", a.dialogueID, a.reviewSeq))
	if err != nil {
		return dialoguetui.CommandResult{},
			fmt.Errorf("review resource URI: %w", err)
	}

	buf := reviewBuffer(ctx, review, a.parser)
	baseline := buf.String()
	edh, err := a.editor.Edit(ctx, uri, buf, false, false)
	if err != nil {
		return dialoguetui.CommandResult{},
			fmt.Errorf("open review editor: %w", err)
	}

	var (
		win  browserapi.Window
		once sync.Once
	)
	bhandler := browserapi.FuncHandler(
		a.reviewChangesKeyHandler(edh),
		func() (err error) {
			once.Do(func() {
				if edited := buf.String(); edited != baseline {
					a.attachFn(dialoguetui.Attachment{
						ID:      attachID,
						Name:    attachName,
						Icon:    chatReviewAttachmentIcon,
						Content: edited,
					})
				}
				err = errors.Join(edh.Close(), a.wm.CloseWindow(win))
			})
			return err
		})
	floating := browserapi.FuncFloating(bhandler, edh.Dimensions)

	win, err = a.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		_ = edh.Close()
		return dialoguetui.CommandResult{},
			fmt.Errorf("open floating window: %w", err)
	}
	return dialoguetui.CommandResult{}, nil
}

// reviewChangesKeyHandler makes the review window closable. <c-w> always
// closes it, mirroring Rune's tabclose binding. Esc closes it too, but
// only for a modeless editor: a modal editor needs Esc to leave insert
// mode.
func (a *commandAdapter) reviewChangesKeyHandler(edh text.Handler) tui.Handler {
	return handler.Wrap(edh, func(ev term.Event) (exit, handled bool) {
		if ev.Type == term.EventKey && !edh.IsSearchMode() {
			closing := ev.Ch == 'w' && ev.Mod == term.ModCtrl
			closing = closing || (ev.Key == term.KeyEsc && !a.editorModal)
			if closing {
				return true, true
			}
		}
		return edh.Handle(ev)
	})
}

func (a *commandAdapter) openCommandFloating(items []component.Responsive) {
	// When the single item is a markdown component, use the markdown handler
	// which provides rich navigation (vi keys, search, mouse selection).
	if len(items) == 1 {
		if md, ok := items[0].(*markdown.Component); ok {
			a.openMarkdownFloating(md)
			return
		}
	}

	list := component.NewResponsiveList()
	for _, item := range items {
		list.PushBack(item)
	}

	listHandler := handler.NopFromComponent(list)
	scrollHandler := handler.Wrap(listHandler, func(ev term.Event) (exit, handled bool) {
		if ev.Type != term.EventKey {
			return
		}
		switch ev.Key {
		case term.KeyEsc:
			return true, true
		case term.KeyArrowUp:
			list.SeekUp()
			return false, true
		case term.KeyArrowDown:
			list.SeekDown()
			return false, true
		}
		if ev.Mod == term.ModCtrl {
			switch ev.Ch {
			case 'k', 'p':
				list.SeekUp()
				return false, true
			case 'j', 'n':
				list.SeekDown()
				return false, true
			}
		}
		return
	})

	var win browserapi.Window
	bhandler := browserapi.FuncHandler(scrollHandler, func() error {
		return a.wm.CloseWindow(win)
	})
	floating := browserapi.FuncFloating(bhandler, func() (int, int) {
		h := list.Height(floatingWidth)
		return floatingWidth, min(h, floatingHeight)
	})
	var err error
	win, err = a.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		slog.Error("command floating window", "error", err)
	}
}

func (a *commandAdapter) openMarkdownFloating(md *markdown.Component) {
	mdh := mdhandler.New(md)

	var win browserapi.Window
	bhandler := browserapi.FuncHandler(mdh, func() error {
		return a.wm.CloseWindow(win)
	})
	floating := browserapi.FuncFloating(bhandler, mdh.Dimensions)
	var err error
	win, err = a.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		slog.Error("command floating window", "error", err)
	}
}

func (a *commandAdapter) Complete(
	ctx context.Context, name string, args []string,
) (iterator.Iterator[string], error) {
	if name == "compact" && len(args) > 1 {
		return iterator.FromSlice[string](nil), nil
	}
	if (name == "model" || name == "compact") && a.llmSvc != nil {
		// Qualified to disambiguate name collisions across providers
		// (e.g. openai/gpt-5.5 vs codex/gpt-5.5).
		return iterator.Map(a.llmSvc.Models(), func(e llmapi.ModelEntry) string {
			return e.Provider + "/" + e.Name
		}), nil
	}
	if name == "effort" {
		levels := make([]string, len(validEffortLevels))
		for i, l := range validEffortLevels {
			levels[i] = string(l)
		}
		return iterator.FromSlice(levels), nil
	}
	return a.handler.Complete(ctx, name, args)
}
