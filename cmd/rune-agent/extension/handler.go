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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdimage "image"
	"io"
	"log/slog"
	"maps"
	"math/rand"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/audit"
	"unstable.build/rune/cmd/rune-agent/agent/geminitools"
	"unstable.build/rune/cmd/rune-agent/agent/taskstore"
	"unstable.build/rune/cmd/rune-agent/configedit"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agentshell"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/hooks"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"

	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
	"unstable.build/rune/cmd/rune-agent/memory"
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/component/template"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/extutil"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"

	tconfig "unstable.build/rune/internal/config"
)

const (
	commandQuery = "?"
	commandChat  = "agent"

	// chatScheme is the scheme of the chat tab URIs, rune-agent://<model>/<id>.
	chatScheme = "rune-agent"

	commandEffort        = "chateffort"
	commandMaxTokens     = "chatmaxtokens"
	commandSkill         = "chatskill"
	commandModel         = "chatmodel"
	commandClear         = "chatclear"
	commandCompact       = "chatcompact"
	commandFork          = "chatfork"
	commandReviewChanges = "chatreviewchanges"
	commandReviewAll     = "chatreviewall"
	commandExport        = "chatexport"
	commandLog           = "chatlog"
	commandAddSymbol     = "chataddsymbol"
)

// Floating windows opened by the agent are capped rather than sized to
// their content: prose stretched across a wide terminal is unreadable,
// and long output should scroll instead of covering the workspace.
const (
	floatingWidth  = 90
	floatingHeight = 45
)

// Router-side model aliases the agent resolves to for its own concepts.
// Each is managed through `models alias` and falls back to `default`
// when unset.
const (
	queryModelAlias   = "query"
	compactModelAlias = "compact"
	dreamModelAlias   = "dream"
)

// defaultPinnedUserMsgMaxLines keeps the pinned message to a glimpse of
// what was asked, as the reply it heads is what is being read.
const defaultPinnedUserMsgMaxLines = 2

var (
	defaultComponentCfg = dialoguetui.ComponentConfig{
		MessagesRowConfig: component.SpanConfig{
			PadHorizontal:    -80,
			PadVertical:      2,
			ContentAlignment: component.AlignmentCentered,
		},
		InputRowColumns: 10,
		InputBox: dialoguetui.InputBoxConfig{
			Placeholder: "Message your assistant...",
			PlaceholderConfig: component.StringResponsiveConfig{
				NoSplitWords: true,
				StringConfig: component.StringConfig{
					Alignment:            component.AlignmentLeft,
					Attributes:           term.Attributes{Fg: term.ColorGray},
					BackgroundAttributes: term.Attributes{},
				},
			},
			ContentAttr: term.Attributes{},
			FrameAttr:   term.Attributes{},
		},
		ReceiveMessageStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorSilver},
			BackgroundAttributes: term.Attributes{},
		},
		ReceiveMessageSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		ReasoningStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.GetColor("dimgray")},
			BackgroundAttributes: term.Attributes{},
		},
		ReasoningSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{},
			BackgroundAttributes: term.Attributes{},
		},
		SendMessageSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageBottomPad: 1,
		SendMessageIcon:      dialoguetui.DefaultSendMessageIcon,
		PinnedMessageMaxRows: defaultPinnedUserMsgMaxLines,
		ToolCallStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorPurple},
			BackgroundAttributes: term.Attributes{},
		},
		ToolCallSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		ToolCallArgsStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorGray},
			BackgroundAttributes: term.Attributes{},
		},
		ToolResultStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorGray},
			BackgroundAttributes: term.Attributes{},
		},
		ToolResultSpanConfig: component.SpanConfig{
			ContentAlignment: component.AlignmentLeft,
		},
		ErrorStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorRed},
			BackgroundAttributes: term.Attributes{},
		},
		ErrorSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		WarningStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorYellow},
			BackgroundAttributes: term.Attributes{},
		},
		WarningSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		CommandOutputSpanConfig: component.SpanConfig{
			PadVertical: 1,
		},
		ToolResultMaxLines: 5,
		StartCollapsed:     true,
		ReasoningAnnotationStringConfig: component.StringConfig{
			Alignment:  component.AlignmentLeft,
			Attributes: term.Attributes{Attrs: term.AttrDim},
		},
		MarkdownConfig: dialoguetui.DefaultMarkdownConfig(),
		PromptToolCallStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			Attributes:           term.Attributes{Fg: term.ColorAqua},
			BackgroundAttributes: term.Attributes{},
		},
		SelectionConfig: dialoguetui.SelectionConfig{
			TitleStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.ColorSilver},
			},
			OptionStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.ColorSilver},
			},
			CursorStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Attrs: term.AttrReverse},
			},
			HeaderStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.ColorYellow},
			},
			DescStringConfig: component.StringConfig{
				Alignment:  component.AlignmentLeft,
				Attributes: term.Attributes{Fg: term.GetColor("dimgray")},
			},
		},
		// Mirrors the command prompt overlay shipped in cmd/rune/rune.star
		// ("command.*_attr") rather than the library fallbacks, which the
		// shipped config overrides.
		CompletionMatchedTextAttr: term.Attributes{
			Fg: term.ColorBlue, Attrs: term.AttrBold,
		},
		CompletionFocusElementAttr: term.Attributes{Fg: term.ColorPurple},
		CompletionElementAttr:      term.Attributes{Attrs: term.AttrDim},
		// Deliberately neutral rather than the command prompt's red: the
		// shader bakes its blend into literal RGB inside this process,
		// which has no access to the host's themed palette, so a hue would
		// clash with themes that remap it.
		CompletionRadarColor: term.GetColor("dimgray"),
		InlineAttachmentAttr: term.Attributes{
			Fg: term.ColorAqua, Attrs: term.AttrUnderline,
		},
	}
)

func registerOpenAITools(
	registry *agent.Registry, fs workspaceapi.FileSystem, cwd workspaceapi.URI,
	searcher searchapi.Searcher, tracker *agentools.FileTracker,
	sessions *agentools.SessionManager, cfg configedit.Config,
) {
	for _, provider := range []string{"openai", "codex"} {
		registry.RegisterOverrides(provider,
			agentools.NewListDir(fs, cwd),
			agentools.NewWriteStdin(sessions),
			agentools.NewGrepFiles(fs, cwd, tracker, searcher),
		)
		registry.RegisterReplacement(provider, "bash",
			agentools.NewExecCommand(sessions, cwd, cfg))
	}
}

func defaultMarkdownConfig() *markdown.Config {
	cfg := markdown.DefaultConfig()
	cfg.HeaderPrefix = false
	return &cfg
}

// loadMCPConfig reads the workspace .mcp.json. A missing or malformed
// file simply means no MCP servers.
func loadMCPConfig(fs workspaceapi.FileSystem, root string) (runemcp.Config, bool) {
	data, err := readWorkspaceFile(fs, filepath.Join(root, ".mcp.json"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("mcp: failed to read .mcp.json", "error", err)
		}
		return runemcp.Config{}, false
	}
	cfg, err := runemcp.LoadConfig(data)
	if err != nil {
		slog.Warn("mcp: failed to parse .mcp.json", "error", err)
		return runemcp.Config{}, false
	}
	return cfg, len(cfg.MCPServers) > 0
}

func defaultAgentsConfig(systemPrompt string) *agent.Cfg {
	return agent.NewConfig([]agent.Definition{
		{
			ID:           "default",
			Name:         "Default Agent",
			SystemPrompt: systemPrompt,
			AllowAny:     true,
		},
	})
}

// resolveMaxTokens reads max_tokens, the global max-output-token budget
// the console's `max_tokens` command persists. Zero means the
// provider/config default; non-positive or malformed values map to it.
func resolveMaxTokens(pconfig config.Config) int {
	v, err := pconfig.GetInt("max_tokens")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'max_tokens' from config", "error", err)
		}
		return 0
	}
	return max(0, v)
}

func newCommandEventHandler(
	ctx context.Context, ed textapi.Editor, w *extensionapi.Workspace,
	pconfig config.Config,
) (ret *aiEditorHandler, err error) {
	fs := w.FileSystem(ctx)
	executor := w.Executor(ctx)
	terminal := w.Terminal(ctx)
	cwd, err := fs.URI(".")
	if err != nil {
		return nil, fmt.Errorf("get workspace root: %w", err)
	}

	var toolsCfg agentools.Config
	if v, err := pconfig.GetInt("max_line_bytes"); err == nil {
		toolsCfg.MaxLineBytes = v
	} else if err != nil && !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'max_line_bytes' from config", "error", err)
	}

	lsp := w.LSP(ctx)
	searcher := w.Search(ctx)
	cfg := configedit.NewConfig(fs, cwd, pconfig)
	tools, tracker := agentools.DefaultTools(fs, executor, cwd, lsp, searcher, toolsCfg, cfg)

	parser := w.Parser(ctx)
	tools = append(tools, agentools.LSPTools(lsp, fs, parser, cwd, tracker)...)
	tools = append(tools, agentools.SyntaxTools(parser, fs, cwd, tracker)...)

	mcpManager := runemcp.NewManager(executor, cwd.Path())
	mcpCfg, hasMCP := loadMCPConfig(fs, cwd.Path())

	// Discover skills from config dirs.
	var skillDirs []string
	if dirs, err := pconfig.GetSlice("skills"); err == nil {
		for _, d := range dirs {
			if s, ok := d.(string); ok {
				skillDirs = append(skillDirs, s)
			}
		}
	}
	skillRegistry := skills.NewRegistry(fs, cwd, skillDirs, w.Notifications(ctx))
	if loaded := skillRegistry.List(); len(loaded) > 0 {
		slog.Info("loaded skills", "count", len(loaded))
	}
	tools = append(tools, agentools.NewSkillTool(skillRegistry, nil, nil))

	// Always register memory tools — the workspace may be bootstrapped
	// after the extension starts. Tools fail gracefully at runtime.
	memoryPath := filepath.Join(w.DataDir(ctx), "memory")
	tools = append(tools, agentools.MemoryTools(executor, memoryPath)...)

	db := w.Storage(ctx)
	sessionsDir := filepath.Join(w.DataDir(ctx), "sessions")
	dialogueStore := dialoguemanager.NewStore(db, sessionsDir)
	tools = append(tools, agentools.ConversationTools(dialogueStore, fs, sessionsDir)...)

	ret = new(aiEditorHandler)
	ret.ctx, ret.cancelCtx = context.WithCancel(context.Background())
	ret.ed = ed
	ret.config = cfg
	ret.mcpManager = mcpManager
	ret.baseTools = tools
	ret.toolRegistry = agent.NewRegistry(tools...)
	ret.executor = executor
	ret.sessionMgr = agentools.NewSessionManager(ret.ctx, executor, terminal)
	registerOpenAITools(ret.toolRegistry, fs, cwd, searcher, tracker, ret.sessionMgr, cfg)
	geminitools.Register(ret.toolRegistry)
	ret.systemPrompt = agent.DefaultSystemPrompt(cwd)
	ret.attribution = agent.DefaultAttribution()
	if attribution, attributionErr := pconfig.GetMap("attribution"); attributionErr == nil {
		if commit, ok := attribution["commit"]; ok {
			if commitText, ok := commit.(string); ok {
				ret.attribution.Commit = &commitText
			} else {
				slog.Warn("get 'attribution.commit' from config", "error", "value must be a string")
			}
		}
	} else if !errors.Is(attributionErr, config.ErrNotFound) {
		slog.Warn("get 'attribution' from config", "error", attributionErr)
	}

	// Discover and load project instruction files (e.g. AGENTS.md).
	agentsFile := agent.DefaultAgentsFile
	if v, err := pconfig.GetString("agents_file"); err == nil {
		agentsFile = v
	} else if err != nil && !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'agents_file' from config", "error", err)
	}
	if agentsFile != "" {
		paths := agent.DiscoverAgentsFiles(fs, cwd.Path(), agentsFile)
		if section := agent.LoadAgentsFiles(fs, paths); section != "" {
			ret.projectInstructions = section
			slog.Info("loaded project instructions", "file", agentsFile, "count", len(paths))
		}
	}

	ret.skillRegistry = skillRegistry
	ret.plansDir = filepath.Join(w.DataDir(ctx), "plans")
	if dir, err := webBrowserDir(ctx, w); err != nil {
		slog.Warn("web_browser tool unavailable", "error", err)
	} else {
		ret.browserDir = dir
	}
	if dir, err := imageGenDir(ctx, w); err != nil {
		slog.Warn("imagegen tool unavailable", "error", err)
	} else {
		ret.imagesDir = dir
	}
	ret.memoryPath = memoryPath
	ret.cwd = cwd
	ret.fs = fs
	ret.exec = executor
	ret.gitID = resolveGitIdentity(ctx, executor, cwd)
	ret.lsp = lsp
	ret.parser = parser
	ret.memoryDataPath = filepath.Join(w.DataDir(ctx), "memory")
	// Pass the alias name through unresolved; the router resolves it per
	// request so a `models alias` change takes effect without a restart.
	ret.defaultModel = llmapi.DefaultModel
	if v, err := pconfig.GetInt("max_tool_output_bytes"); err == nil {
		ret.maxToolOutputBytes = v
	} else if err != nil && !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'max_tool_output_bytes' from config", "error", err)
	}
	if v, err := pconfig.GetFloat("auto_compact_ratio"); err == nil {
		ret.autoCompactRatio = v
	} else if err != nil && !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'auto_compact_ratio' from config", "error", err)
	}
	ret.defaultMaxTokens = resolveMaxTokens(pconfig)
	ret.reviewContextLines = resolveReviewContextLines(pconfig)
	ret.git = vctrl.NewGitCommand(cwd, executor, fs)
	// The host-provided llmapi.Service (w.LLM(ctx)) owns provider auth,
	// llama.cpp, codex login, and custom_provider routing; rune-agent
	// no longer constructs its own registry.
	ret.llmSvc = w.LLM(ctx)

	// Hooks: parse extensions.rune-agent.config.hooks. Errors are
	// non-fatal: the extension still starts with an empty (no-op)
	// hook config. The single Runner constructed here is shared
	// across chat and query agents and propagated via agent.Config.
	noti := w.Notifications(ctx)
	var hooksCfg hooks.Config
	if hooksMap, hookErr := pconfig.GetMap("hooks"); hookErr == nil {
		parsed, parseErr := hooks.Parse(hooksMap)
		if parseErr != nil {
			_, _ = noti.Notify(browserapi.LevelWarn,
				"hooks: invalid config: %v", parseErr)
		} else {
			hooksCfg = parsed
		}
	} else if !errors.Is(hookErr, config.ErrNotFound) {
		_, _ = noti.Notify(browserapi.LevelWarn,
			"hooks: invalid config: %v", hookErr)
	}
	ret.hookRunner = hooks.NewRunner(hooksCfg, executor, noti, cwd.Path())

	hostConfig := w.Config(ctx)
	ret.cfg = defaultComponentCfg
	ret.cfg.MarkdownConfig.Parser = w.Parser(ctx)
	backgroundAttr, err := config.GetAttributes(pconfig, "background_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'background_attr' from extension config", "error", err)
		}
	}
	ret.cfg.ReceiveMessageStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.InputBox.PlaceholderConfig.BackgroundAttributes = backgroundAttr

	ret.cfg.InputBox.FrameAttr.Bg = backgroundAttr.Bg
	ret.cfg.InputBox.PlaceholderConfig.Bg = backgroundAttr.Bg
	ret.cfg.InputBox.ContentAttr.Bg = backgroundAttr.Bg

	ret.cfg.ReceiveMessageStringConfig.Bg = backgroundAttr.Bg

	sendMsgBackgroundAttr, err := config.GetAttributes(pconfig, "user_msg_background_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'user_msg_background_attr' from extension config", "error", err)
		}
		sendMsgBackgroundAttr = term.Attributes{Bg: term.ColorGray}
	}
	ret.cfg.SendMessageStringConfig.BackgroundAttributes = sendMsgBackgroundAttr
	ret.cfg.SendMessageStringConfig.Bg = sendMsgBackgroundAttr.Bg
	ret.cfg.ReasoningStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.ReasoningStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.ToolCallStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.ToolCallStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.ToolCallArgsStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.ToolCallArgsStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.ToolResultStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.ToolResultStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.ReasoningAnnotationStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.ReasoningAnnotationStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.ErrorStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.ErrorStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.WarningStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.WarningStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.PromptToolCallStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.PromptToolCallStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.SelectionConfig.TitleStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.SelectionConfig.TitleStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.SelectionConfig.OptionStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.SelectionConfig.OptionStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.SelectionConfig.CursorStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.SelectionConfig.CursorStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.SelectionConfig.HeaderStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.SelectionConfig.HeaderStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.SelectionConfig.DescStringConfig.BackgroundAttributes = backgroundAttr
	ret.cfg.SelectionConfig.DescStringConfig.Bg = backgroundAttr.Bg
	ret.cfg.BackgroundColor = backgroundAttr.Bg
	ret.backgroundAttr = backgroundAttr

	sendMsgAttr, err := config.GetAttributes(pconfig, "user_msg_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'user_msg_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.SendMessageStringConfig.Attributes = sendMsgAttr
	}
	ret.cfg.PinnedMessageMaxRows = resolvePinnedUserMsgMaxLines(pconfig)
	ret.cfg.MessagesRowConfig.PadHorizontal = resolveTranscriptHorizontalPadding(pconfig)
	ret.cfg.SendMessageIcon = resolveUserMsgIcon(ret.cfg.SendMessageIcon, pconfig)
	recvMsgAttr, err := config.GetAttributes(pconfig, "assistant_msg_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'assistant_msg_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.ReceiveMessageStringConfig.Attributes = recvMsgAttr
	}

	inputBoxAttr, err := config.GetAttributes(pconfig, "input_box_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'input_box_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.InputBox.ContentAttr = inputBoxAttr
	}
	ret.cfg.InputBackgroundColor = resolveInputBackgroundColor(
		ret.cfg.InputBackgroundColor, pconfig)
	inputBoxPlaceholderAttr, err := config.GetAttributes(pconfig, "input_box_placeholder_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("Error getting 'input_box_placeholder_attr'"+
				" from extension config", "error", err)
		}
	} else {
		ret.cfg.InputBox.PlaceholderConfig.Attributes = inputBoxPlaceholderAttr
	}

	inputBoxFrameAttr, err := config.GetAttributes(pconfig, "input_box_frame_attr")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("Error getting 'input_box_frame_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.InputBox.FrameAttr = inputBoxFrameAttr
	}

	ret.cfg.InputBox.FrameCharSet = inputBoxFrameCharSet(pconfig)

	inputBgColor, err := pconfig.GetColor("input_background_color")
	if err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'input_background_color' from extension config", "error", err)
		}
	} else {
		ret.cfg.InputBackgroundColor = inputBgColor
	}

	if attr, err := config.GetAttributes(pconfig, "completion_matched_text_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_matched_text_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.CompletionMatchedTextAttr = attr
	}
	if attr, err := config.GetAttributes(pconfig, "completion_focus_element_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_focus_element_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.CompletionFocusElementAttr = attr
	}
	if attr, err := config.GetAttributes(pconfig, "completion_element_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_element_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.CompletionElementAttr = attr
	}
	if col, err := pconfig.GetColor("completion_radar_color"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'completion_radar_color' from extension config", "error", err)
		}
	} else {
		ret.cfg.CompletionRadarColor = col
	}
	if attr, err := config.GetAttributes(pconfig, "inline_attachment_attr"); err != nil {
		if err != config.ErrNotFound {
			slog.Warn("get 'inline_attachment_attr' from extension config", "error", err)
		}
	} else {
		ret.cfg.InlineAttachmentAttr = attr
	}
	ret.cfg.StatusBar = statusBarConfig(pconfig, hostConfig, noti)
	ret.cfg.StatusBar.DurationPrecision = ret.cfg.DurationPrecision

	ret.queryDefaultModel = queryModelAlias

	ret.compactModel = compactModelAlias

	ret.clip = text.NewSystemClipboard()
	ret.cfg.Clipboard = ret.clip

	// Resolve the configured editor for composing messages. On error the
	// compose editor stays nil and dialoguetui falls back to its inputbox.
	if editor, cerr := extutil.Editor(ret.clip, hostConfig); cerr != nil {
		slog.Warn("resolve dialogue editor, using inputbox", "error", cerr)
	} else {
		ret.cfg.Editor = editor
		if modal, merr := extutil.EditorModal(hostConfig); merr != nil {
			slog.Warn("resolve dialogue editor mode", "error", merr)
		} else {
			ret.cfg.EditorModal = modal
		}
		ret.cfg.ModalStartInsert = true
		if startInsert, serr := pconfig.GetBool("editor_modal_start_insert"); serr == nil {
			ret.cfg.ModalStartInsert = startInsert
		} else if !errors.Is(serr, config.ErrNotFound) {
			slog.Warn("get 'editor_modal_start_insert' from config", "error", serr)
		}
	}

	ret.db = db
	ret.cfg.StatusBar.Storage = storageapi.WithPartition(db, statusBarImagesPartition)
	ret.cfg.InputBox.GraphicsStorage = storageapi.WithPartition(db, frameImagesPartition)
	ret.imageCache = storageapi.WithPartition(db, transcriptImagesPartition)
	if auditEnabled, _ := pconfig.GetBool("audit_enabled"); auditEnabled {
		ret.auditStore = audit.NewStore(ret.db)
	}
	ret.p = w.Interrupter(ctx)
	ret.o = w.ResourceOpener(ctx)
	ret.wm = w.WindowManager(ctx)
	// Wrap Notifications so every Notify call fans out to configured
	// Notification hooks. The wrapper is purely observational; the
	// hook return value is ignored.
	ret.n = newNotificationsWithHooks(
		w.Notifications(ctx), ret.hookRunner, ret.cwd)
	ret.resources = make(map[string]string)

	ret.dialogueStore = dialogueStore

	if compactSvc, _, cerr := ret.modelService(ctx, ret.compactModel); cerr != nil {
		_, _ = noti.Notify(browserapi.LevelWarn,
			"failed to create backend for compact model (%v), compaction will use the chat model",
			cerr)
		ret.compactModel = ""
		ret.compactSvc = nil
	} else {
		ret.compactSvc = compactSvc
	}

	queryService, queryEntry, err := ret.modelService(ctx, ret.queryDefaultModel)
	if err != nil {
		first := firstModel(ret.llmSvc)
		if first == "" || first == ret.queryDefaultModel {
			return nil, fmt.Errorf("new backend for query dialogues: %v", err)
		}
		_, _ = noti.Notify(browserapi.LevelWarn,
			"failed to create backend for model %q (%v), falling back to %q",
			ret.queryDefaultModel, err, first)
		ret.queryDefaultModel = first
		queryService, queryEntry, err = ret.modelService(ctx, first)
		if err != nil {
			return nil, fmt.Errorf("new backend for query dialogues (fallback %q): %v", first, err)
		}
	}
	ret.queryAgent = agent.NewAgent(
		queryService, ret.toolRegistry, ret.skillRegistry,
		ret.dialogueStore, agent.NoMemory(), agent.Config{
			MaxToolOutputBytes:  ret.maxToolOutputBytes,
			AutoCompactRatio:    ret.autoCompactRatio,
			CompactSvc:          ret.compactSvc,
			SystemPrompt:        agent.QuerySystemPrompt(ret.cwd) + agent.ProviderToolAddendum(queryEntry.Provider),
			Attribution:         ret.attribution,
			ProjectInstructions: ret.projectInstructions,
			SessionKey:          "query",
			AgentID:             "query",
			Model:               queryEntry,
			Workspace:           ret.cwd,
			Hooks:               ret.hookRunner,
		},
	)
	ret.queryAgent.SetMaxOutputTokens(ret.defaultMaxTokens)

	ret.agentsConfig = defaultAgentsConfig(ret.systemPrompt)

	// Starting an MCP server needs StartCommand authorization, which
	// blocks until the user answers a prompt. Connect in the background
	// so the agent registers its commands immediately.
	if hasMCP {
		mcpManager.OnServerExit = func(name, detail string) {
			_, _ = ret.n.Notify(browserapi.LevelError,
				"MCP server %q %s", name, detail)
		}
		go debug.CapturePanicReport(func() {
			ret.connectMCPServers(mcpCfg)
		})
	}

	return ret, nil
}

// newLinkClickHandler shows http, https and file links through o, resolving
// scheme-less file paths against cwd. A file link to a source location then
// moves the cursor of the editor ed returns for it. Fragment-only links,
// network references and other schemes are left to the dialogue.
func newLinkClickHandler(
	mu sync.Locker,
	cwd workspaceapi.URI,
	o browserapi.ResourceOpener,
	ed cursorEditor,
	noti browserapi.Notifications,
) func(*url.URL) bool {
	return func(link *url.URL) bool {
		switch link.Scheme {
		case "http", "https", "file":
		case "":
			if link.Path == "" || link.Host != "" || link.User != nil {
				return false
			}
		default:
			return false
		}
		// The dialogue calls this holding mu, which its stream consumer needs
		// too, and the opener, editor and Notify wait on the host's lock.
		// Relock in a defer so a panic still hands mu back to the dialogue's
		// deferred Unlock.
		mu.Unlock()
		defer mu.Lock()
		if err := showDialogueLink(cwd, link, o, ed); err != nil {
			_, _ = noti.Notify(browserapi.LevelError, "open %s: %v", link, err)
		}
		return true
	}
}

func showDialogueLink(
	cwd workspaceapi.URI, link *url.URL, o browserapi.ResourceOpener, ed cursorEditor,
) error {
	uri, pos, err := dialogueLinkURI(cwd, link)
	if err != nil {
		return err
	}
	if err := o.Show(uri); err != nil {
		return err
	}
	if pos == nil {
		return nil
	}
	// Show also succeeds for resources it routes to another workspace,
	// whose editors ed cannot reach, so a failed lookup is reported as
	// the navigation failing rather than the file failing to open.
	eh, err := ed.Editor(uri)
	if err == nil {
		err = ed.SetCursor(eh, *pos)
	}
	if err != nil {
		return fmt.Errorf("go to %d:%d: %w", pos.Y+1, pos.X+1, err)
	}
	return nil
}

// dialogueLinkURI resolves link against the workspace directory cwd. When
// the last path segment of a file or scheme-less link ends in :line or
// :line:column, the location is removed from the URI and returned as
// zero-based coordinates, which are nil for links without one. Only
// unescaped colons delimit a location, so %3A keeps a colon in the file name.
func dialogueLinkURI(
	cwd workspaceapi.URI, link *url.URL,
) (workspaceapi.URI, *term.Coordinates, error) {
	var pos *term.Coordinates
	if link.Scheme == "" || link.Scheme == "file" {
		escaped, loc, err := splitSourceLocation(link.EscapedPath())
		if err != nil {
			return workspaceapi.URI{}, nil, err
		}
		if loc != nil {
			stripped := *link
			if stripped.Path, err = url.PathUnescape(escaped); err != nil {
				return workspaceapi.URI{}, nil, err
			}
			stripped.RawPath = escaped
			link, pos = &stripped, loc
		}
	}
	if link.Scheme == "" {
		base, err := url.Parse(cwd.String())
		if err != nil {
			return workspaceapi.URI{}, nil, err
		}
		// The workspace URI denotes a directory even without a trailing slash.
		base.Path = strings.TrimSuffix(base.Path, "/") + "/"
		base.RawPath = ""
		link = base.ResolveReference(link)
	}
	uri, err := workspaceapi.ParseURI(link.String())
	return uri, pos, err
}

// splitSourceLocation splits a one-based :line or :line:column suffix off
// the last segment of the escaped path p. A colon followed by anything but
// a decimal number is part of the file name. A number that cannot be a
// position, such as zero, a signed number or one overflowing int32, is an
// error rather than a file name, so the link never opens a file named after
// part of its location.
func splitSourceLocation(p string) (string, *term.Coordinates, error) {
	dir, file := path.Split(p)
	fields := strings.Split(file, ":")
	n := len(fields)
	if n < 2 || !isLocationField(fields[n-1]) {
		return p, nil, nil
	}
	loc := fields[n-1:]
	if n > 2 && isLocationField(fields[n-2]) {
		loc = fields[n-2:]
	}
	name := strings.Join(fields[:n-len(loc)], ":")
	suffix := ":" + strings.Join(loc, ":")
	if name == "" {
		return "", nil, fmt.Errorf("source location %q names no file", suffix)
	}
	lineCol := [2]int{1, 1}
	for i, f := range loc {
		v, err := strconv.ParseInt(f, 10, 32)
		if err != nil || v < 1 || f[0] < '0' || f[0] > '9' {
			return "", nil, fmt.Errorf("invalid source location %q", suffix)
		}
		lineCol[i] = int(v)
	}
	return dir + name, &term.Coordinates{X: lineCol[1] - 1, Y: lineCol[0] - 1}, nil
}

// isLocationField reports whether f is written as a decimal number, signed
// or not, whether or not it is a valid position.
func isLocationField(f string) bool {
	if f != "" && (f[0] == '+' || f[0] == '-') {
		f = f[1:]
	}
	return f != "" && strings.Trim(f, "0123456789") == ""
}

const transcriptImagesPartition = "transcript-images"

// frameImagesPartition caches the images of the compose box frame.
const frameImagesPartition = "frame_images"

// inputBoxFrameCharSet reads input_box_frame_charset from pconfig, whose
// missing slots keep those of the default frame. An invalid charset is
// the default one.
func inputBoxFrameCharSet(pconfig config.Config) component.FrameCharSet {
	def := defaultComponentCfg.InputBox.FrameCharSet
	if def == (component.FrameCharSet{}) {
		def = component.FrameCharSetDefault()
	}
	cs, err := tconfig.GetFrameCharset(pconfig, "input_box_frame_charset", def)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		slog.Warn("get 'input_box_frame_charset' from extension config", "error", err)
	}
	return cs
}

// refreshFrameImages reads the images g names again, dropping the copies
// storage caches, and returns them by URI. An image that does not load
// is left out.
func refreshFrameImages(
	ctx context.Context, g component.FrameCharSetGraphics, storage storageapi.Service,
) map[string]stdimage.Image {
	images := make(map[string]stdimage.Image)
	tried := make(map[string]struct{})
	for _, uri := range []string{
		g.TopLeft, g.TopRight, g.BottomLeft, g.BottomRight,
		g.HorizontalTop, g.HorizontalBottom, g.VerticalLeft, g.VerticalRight,
	} {
		if _, ok := tried[uri]; ok || uri == "" {
			continue
		}
		tried[uri] = struct{}{}
		img, err := imageuri.Refresh(ctx, uri, template.DefaultImageTTL, storage)
		if err != nil {
			slog.Warn("reload frame image", "error", err)
			continue
		}
		images[uri] = img
	}
	return images
}

// dialogueConfig returns the config for a dialogue whose handler is given
// mu, which the dialogue holds while it calls OnLinkClick.
func (h *aiEditorHandler) dialogueConfig(mu sync.Locker) dialoguetui.ComponentConfig {
	h.statusBarMu.Lock()
	cfg := h.cfg
	h.statusBarMu.Unlock()
	cfg.OnLinkClick = newLinkClickHandler(mu, h.cwd, h.o, h.ed, h.n)
	md := dialoguetui.DefaultMarkdownConfig()
	if cfg.MarkdownConfig != nil {
		shared := *cfg.MarkdownConfig
		md = &shared
	}
	md.FS = h.fs
	md.Dir = h.cwd.Path()
	// Markdown rejects an interrupter without storage, so set them together.
	if h.imageCache != nil {
		md.Storage = h.imageCache
		md.Interrupter = h.p
	}
	cfg.MarkdownConfig = md
	return cfg
}

type aiEditorHandler struct {
	exit         atomic.Uint32
	llmSvc       llmapi.Service
	defaultModel string
	// statusBarMu guards cfg.StatusBar and cfg.InputBox.FrameCharSet,
	// which a config reload replaces.
	statusBarMu         sync.Mutex
	cfg                 dialoguetui.ComponentConfig
	backgroundAttr      term.Attributes
	dialogueStore       dialoguemanager.Store
	queryAgent          *agent.Agent
	queryDefaultModel   string
	compactModel        string // empty means use the chat model
	compactSvc          llmapi.Service
	toolRegistry        *agent.Registry
	systemPrompt        string
	attribution         agent.Attribution
	projectInstructions string
	agentsConfig        *agent.Cfg
	baseToolsMu         sync.RWMutex
	baseTools           []agent.Tool
	chatRegistries      map[string][]*agent.Registry
	mcpManager          *runemcp.Manager
	sessionMgr          *agentools.SessionManager

	maxToolOutputBytes int
	autoCompactRatio   float64
	reviewContextLines int

	resources      map[string]string
	clip           clipboard.Register
	ed             cursorEditor
	wm             browserapi.WindowManager
	n              browserapi.Notifications
	o              browserapi.ResourceOpener
	p              term.Interrupter
	db             storageapi.Service
	imageCache     storageapi.Service
	config         configedit.Config
	skillRegistry  *skills.SkillRegistry
	plansDir       string
	browserDir     string
	imagesDir      string
	memoryDataPath string
	exec           workspaceapi.Executor
	lsp            semanticapi.LSP
	parser         syntaxapi.Parser
	memoryPath     string
	cwd            workspaceapi.URI
	fs             workspaceapi.FileSystem
	executor       workspaceapi.Executor
	git            vctrl.Service
	gitID          gitIdentity

	auditStore *audit.Store

	// generateDialogueID overrides the spawner's dialogue ID
	// generator. Testing only.
	generateDialogueID func(ctx context.Context, agentID string) string
	// generatePlanPath overrides the plan path generator. Testing only.
	generatePlanPath func(title string) string
	defaultsMu       sync.Mutex
	defaultEffort    llmapi.ReasoningEffort // global default applied to new chats/queries
	defaultMaxTokens int                    // global default applied to new chats/queries

	openChats      sync.Map
	openChatAgents sync.Map
	// lastCursor and lastChatID mirror the caret position and the chat
	// that last had focus. Workspace commands run once the prompt owns
	// the caret and from outside any chat tab, so both have to be read
	// back from state captured as the events happened.
	lastCursor atomic.Value // *cursorLocation
	lastChatID atomic.Value // *string
	// openChatTx maps an open chat's dialogue ID to its dialoguetui event
	// channel, letting workspace command-prompt commands inject in-chat
	// commands into the focused chat. Stored and deleted alongside
	// openChats / openChatAgents.
	openChatTx sync.Map
	ctx        context.Context
	cancelCtx  func()

	// hookRunner dispatches Claude-Code-style hooks. nil when no
	// hooks are configured.
	hookRunner *hooks.Runner
}

// tools returns the current base tool set. MCP servers connect in the
// background, so the set grows after initialization.
func (h *aiEditorHandler) tools() []agent.Tool {
	h.baseToolsMu.RLock()
	defer h.baseToolsMu.RUnlock()
	return slices.Clone(h.baseTools)
}

// addTools appends late-arriving tools to the base set and forwards
// them to every subscribed conversation, so tools from MCP servers that
// finish connecting mid-session reach chats that are already open.
func (h *aiEditorHandler) addTools(tools ...agent.Tool) {
	if len(tools) == 0 {
		return
	}
	h.baseToolsMu.Lock()
	defer h.baseToolsMu.Unlock()
	h.baseTools = append(h.baseTools, tools...)
	h.toolRegistry.Add(tools...)
	for _, regs := range h.chatRegistries {
		for _, r := range regs {
			r.Add(tools...)
		}
	}
}

// subscribeTools registers a conversation's registries to receive tools
// that arrive after its snapshot was taken. The delta between the
// snapshot and the current base set is applied immediately, under the
// same lock addTools uses, so no tool can fall between snapshot and
// subscription.
func (h *aiEditorHandler) subscribeTools(
	id string, snapshotLen int, regs ...*agent.Registry,
) {
	h.baseToolsMu.Lock()
	defer h.baseToolsMu.Unlock()
	if delta := h.baseTools[snapshotLen:]; len(delta) > 0 {
		for _, r := range regs {
			r.Add(delta...)
		}
	}
	if h.chatRegistries == nil {
		h.chatRegistries = make(map[string][]*agent.Registry)
	}
	h.chatRegistries[id] = regs
}

func (h *aiEditorHandler) unsubscribeTools(id string) {
	h.baseToolsMu.Lock()
	defer h.baseToolsMu.Unlock()
	delete(h.chatRegistries, id)
}

// warnPendingMCPServers tells a freshly opened conversation which MCP
// servers are still starting, so missing tools are explained rather
// than silently absent.
func (h *aiEditorHandler) warnPendingMCPServers(tx chan<- dialoguetui.MessageEvent) {
	if h.mcpManager == nil {
		return
	}
	var pending []string
	for _, srv := range h.mcpManager.Servers() {
		if srv.Status == runemcp.StatusConnecting {
			pending = append(pending, srv.Name)
		}
	}
	if len(pending) == 0 {
		return
	}
	tx <- dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventWarning,
		Text: fmt.Sprintf(
			"MCP server(s) still starting: %s. Their tools will join this conversation once connected.",
			strings.Join(pending, ", ")),
	}
}

// connectMCPServers brings up every configured MCP server, keeping the
// user informed: a progress notification stays open while a server
// starts (authorization may hold it for a while), and resolves into a
// success or error notification. Tools reach the base set and every
// open conversation as each server comes up.
func (h *aiEditorHandler) connectMCPServers(cfg runemcp.Config) {
	for _, name := range slices.Sorted(maps.Keys(cfg.MCPServers)) {
		id, nerr := h.n.Notify(browserapi.LevelInfo,
			"Starting MCP server %q…", name)
		if nerr == nil {
			_ = h.n.UpdateNotificationProgress(id, "", 0, 1)
		}
		tools, err := h.mcpManager.ConnectServer(h.ctx, name, cfg.MCPServers[name])
		if nerr == nil {
			_ = h.n.UpdateNotificationProgress(id, "", 1, 1)
		}
		if err != nil {
			_, _ = h.n.Notify(browserapi.LevelError,
				"MCP server %q failed to start: %v", name, err)
			continue
		}
		h.addTools(tools...)
		_, _ = h.n.Notify(browserapi.LevelSuccess,
			"MCP server %q is up: %d tools available", name, len(tools))
	}
}

// modelService resolves a model entry from the host-provided LLM
// service, optionally wrapping it in an audit decorator. Replaces the
// rune-agent-owned newLLMService/newLlamaCppService dispatch logic;
// provider auth, registries, and local-model lifecycle now live in the
// IDE host behind w.LLM(ctx).
func (h *aiEditorHandler) modelService(ctx context.Context, model string) (
	llmapi.Service, llmapi.ModelEntry, error,
) {
	entry, err := llmarg.Resolve(ctx, h.llmSvc, model)
	if err != nil {
		return nil, llmapi.ModelEntry{}, err
	}
	svc := h.llmSvc
	if h.auditStore != nil {
		svc = audit.NewService(svc, h.auditStore, entry)
	}
	return svc, entry, nil
}

func (h *aiEditorHandler) getDefaultEffort() llmapi.ReasoningEffort {
	h.defaultsMu.Lock()
	defer h.defaultsMu.Unlock()
	return h.defaultEffort
}

func (h *aiEditorHandler) setDefaultEffort(e llmapi.ReasoningEffort) {
	h.defaultsMu.Lock()
	h.defaultEffort = e
	h.defaultsMu.Unlock()
	h.queryAgent.SetEffort(e)
}

func (h *aiEditorHandler) getDefaultMaxTokens() int {
	h.defaultsMu.Lock()
	defer h.defaultsMu.Unlock()
	return h.defaultMaxTokens
}

func (h *aiEditorHandler) setDefaultMaxTokens(n int) {
	h.defaultsMu.Lock()
	h.defaultMaxTokens = n
	h.defaultsMu.Unlock()
	h.queryAgent.SetMaxOutputTokens(n)
	h.openChatAgents.Range(func(id, v any) bool {
		if ag, ok := v.(*agent.Agent); ok {
			ag.SetMaxOutputTokens(n)
		}
		if sc, ok := h.openChats.Load(id); ok {
			if sc, ok := sc.(syncComponent); ok {
				sc.setStatusBarState(func(s *dialoguetui.StatusBarState) {
					s.MaxTokens = n
				})
			}
		}
		return true
	})
}

// watchReload reloads the style of the chats from the config fetch
// returns each time reload delivers, until ctx ends. A nil reload never
// delivers.
func (h *aiEditorHandler) watchReload(
	ctx context.Context, reload <-chan os.Signal, fetch func(context.Context) config.Config,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-reload:
		}
		if err := h.reloadStyle(ctx, fetch(ctx)); err != nil {
			slog.Warn("reload the chat style", "error", err)
		}
	}
}

// reloadStyle configures the status bar and the compose box frame of the
// open chats, and of those opened from now on, with the status_bar block
// and input_box_frame_charset host holds for this extension, reading
// their images again. host is the config Rune serves; a host that could
// not be fetched leaves the chats as they are.
func (h *aiEditorHandler) reloadStyle(ctx context.Context, host config.Config) error {
	own, err := ownConfig(host)
	if err != nil {
		return err
	}
	h.statusBarMu.Lock()
	storage := h.cfg.StatusBar.Storage
	frameStorage := h.cfg.InputBox.GraphicsStorage
	h.statusBarMu.Unlock()
	cfg := statusBarConfig(own, host, h.n)
	cfg.DurationPrecision = h.cfg.DurationPrecision
	cfg.Storage = storage
	cfg.Images = refreshStatusBarImages(ctx, cfg.Layout, storage)
	frame := inputBoxFrameCharSet(own)
	var frameImages map[string]stdimage.Image
	if frameStorage != nil {
		frameImages = refreshFrameImages(ctx, frame.Graphics, frameStorage)
	}

	h.statusBarMu.Lock()
	h.cfg.StatusBar = cfg
	h.cfg.InputBox.FrameCharSet = frame
	h.statusBarMu.Unlock()
	h.openChats.Range(func(_, v any) bool {
		// Chats still opening hold no component yet; they read the
		// config they open with afterwards.
		if sc, ok := v.(syncComponent); ok {
			sc.setStatusBarConfig(cfg)
			sc.setInputBoxFrameCharSet(frame, frameImages)
		}
		return true
	})
	return h.p.Interrupt(ctx)
}

func (h *aiEditorHandler) Handle(ctx context.Context, ev textapi.Event) (exit bool) {
	uexit := h.exit.Load()
	exit = uexit != 0
	if exit {
		return
	}
	if ev.URI == (workspaceapi.URI{}) {
		return
	}

	var err error
	switch ev.Type {
	case textapi.EventTypeFlush, textapi.EventTypeOpen:
		content, ok := h.resources[ev.URI.String()]
		if ok {
			err = h.queryAgent.AddContextResource(ctx, ev.URI, content)
		}
		h.resources[ev.URI.String()] = ev.Content
	case textapi.EventTypeFocus:
		content, ok := h.resources[ev.URI.String()]
		if !ok {
			slog.Warn("could not find resource on focus event", "uri", ev.URI)
			return
		}
		err = h.queryAgent.AddContextResource(ctx, ev.URI, content)
	case textapi.EventTypeUnfocus:
		err = h.queryAgent.RemoveContextResource(ctx, ev.URI)
	case textapi.EventTypeClose:
		delete(h.resources, ev.URI.String())
	case textapi.EventTypeCursor:
		h.recordCursor(ev)
	}

	if err != nil {
		slog.Error("query agent context resource", "uri", ev.URI, "error", err)
	}
	return
}

func (h *aiEditorHandler) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) (err error) {
	switch cmd.Name {
	case commandQuery:
		return h.handleQuery(cmd)
	case commandChat:
		return h.handleChat(cmd)
	case commandModel, commandEffort, commandMaxTokens, commandSkill,
		commandClear, commandCompact, commandFork, commandReviewChanges,
		commandReviewAll, commandExport, commandLog:
		return h.routeChatCommand(cmd)
	case commandAddSymbol:
		return h.handleChatAddSymbol(cmd)
	}

	return nil
}

// dialogueIDFromURI extracts the dialogue ID from a rune-agent chat tab URI
// of the form rune-agent://<model>/<id>. It returns false for any other
// scheme or a URI without a dialogue ID path segment.
func dialogueIDFromURI(uri workspaceapi.URI) (string, bool) {
	if uri.Scheme() != chatScheme {
		return "", false
	}
	id := strings.TrimPrefix(uri.Path(), "/")
	if id == "" {
		return "", false
	}
	return id, true
}

// OpenResource satisfies textapi.ResourceOpenHandler. It returns the chat
// uri identifies, resumed with the model the dialogue was stored with when
// that model is still available and the default model otherwise. It fails
// when the chat is already open.
func (h *aiEditorHandler) OpenResource(
	ctx context.Context, uri workspaceapi.URI,
) (browserapi.Handler, error) {
	id, ok := dialogueIDFromURI(uri)
	if !ok {
		return nil, fmt.Errorf("%s is not an agent chat", uri)
	}
	args := []string{id}
	// The URI host is a lossy encoding of the model (see getModelUri), so
	// the model comes from the stored dialogue instead.
	if d, err := h.dialogueStore.Get(ctx, id); err == nil && d.Model != "" {
		if _, err := llmarg.Resolve(h.ctx, h.llmSvc, d.Model); err == nil {
			args = append(args, d.Model)
		}
	}
	c, err := h.newChat(textapi.Command{Name: commandChat, Args: args}, uri)
	if err != nil {
		return nil, err
	}
	return c.content, nil
}

// CompleteResource satisfies textapi.ResourceOpenHandler. It yields the URIs
// starting with prefix of the dialogues chat completes without --all, in the
// order the store lists them. An open chat is offered as the URI of its tab,
// so opening it shows that tab rather than failing as already open.
func (h *aiEditorHandler) CompleteResource(
	ctx context.Context, prefix string,
) (iterator.Iterator[string], error) {
	it, err := h.dialogueStore.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("dialogue store list: %w", err)
	}
	chats := iterator.Filter(it, func(d dialoguemanager.DialogueHeader) bool {
		return d.ID != "" && !d.SubAgent && !h.isForeignDialogue(d)
	})
	return iterator.Filter(iterator.Map(chats, h.chatURI), func(uri string) bool {
		return uri != "" && strings.HasPrefix(uri, prefix)
	}), nil
}

// chatURI returns the URI that opens the chat of d, or "" when it has none.
func (h *aiEditorHandler) chatURI(d dialoguemanager.DialogueHeader) string {
	if v, ok := h.openChats.Load(d.ID); ok {
		if sc, ok := v.(syncComponent); ok && sc.tab.hasTab() {
			return sc.tab.uri.String()
		}
	}
	uri, err := getModelUri(d.ID, cmp.Or(d.Model, h.defaultModel))
	if err != nil {
		// newChat cannot open such a dialogue either.
		return ""
	}
	return uri.String()
}

// routeChatCommand forwards a workspace command-prompt command (the
// chat-prefixed commands like chatmodel, chateffort, chatclear) to the
// focused rune-agent chat, where it runs exactly as if the user had typed
// the equivalent /command in that chat.
func (h *aiEditorHandler) routeChatCommand(cmd textapi.Command) error {
	id, ok := dialogueIDFromURI(cmd.URI)
	if !ok {
		return fmt.Errorf("%s must be run from an open agent chat tab", cmd.Name)
	}
	v, ok := h.openChatTx.Load(id)
	if !ok {
		return fmt.Errorf("%s must be run from an open agent chat tab", cmd.Name)
	}
	tx := v.(chan<- dialoguetui.MessageEvent)

	name, args := cmd.Name, cmd.Args
	switch cmd.Name {
	case commandModel:
		name = "model"
	case commandEffort:
		name = "effort"
	case commandClear:
		if err := rejectPositionalID("clear", cmd.Args); err != nil {
			return err
		}
		name = "clear"
	case commandCompact:
		if len(cmd.Args) > 1 {
			return errors.New("usage: chatcompact [<model>]")
		}
		name = "compact"
	case commandFork:
		if err := rejectPositionalID("fork", cmd.Args); err != nil {
			return err
		}
		name = "fork"
	case commandReviewChanges:
		if err := rejectPositionalID("reviewchanges", cmd.Args); err != nil {
			return err
		}
		name = "reviewchanges"
	case commandReviewAll:
		if err := rejectPositionalID("reviewall", cmd.Args); err != nil {
			return err
		}
		name = "reviewall"
	case commandExport:
		if err := rejectPositionalID("export", cmd.Args); err != nil {
			return err
		}
		name = "export"
	case commandLog:
		if err := rejectPositionalID("log", cmd.Args); err != nil {
			return err
		}
		name = "log"
	case commandMaxTokens:
		// The prompt command "chatmaxtokens" maps to the in-chat adapter's
		// "max_tokens" command name.
		name = "max_tokens"
	case commandSkill:
		// In-chat skills are invoked as /<skill> [args]; the prompt
		// command "chatskill <name> [args]" shifts the first argument
		// into the command name so the chat's skill resolution path fires.
		if len(cmd.Args) == 0 {
			return fmt.Errorf("%s requires a skill name", cmd.Name)
		}
		name, args = cmd.Args[0], cmd.Args[1:]
	}

	ev := dialoguetui.MessageEvent{
		Type:        dialoguetui.MessageEventCommand,
		CommandName: name,
		CommandArgs: args,
	}
	// Sending on tx must not block the event loop, so dispatch the send
	// to a goroutine bounded by the handler context.
	go debug.CapturePanicReport(func() {
		select {
		case tx <- ev:
		case <-h.ctx.Done():
		}
	})
	return nil
}

func (h *aiEditorHandler) Complete(ctx context.Context, name string, args []string) (
	iterator.Iterator[string], error,
) {
	slog.Debug("complete with", "name", name, "args", args)

	// Strip --all flag from args for length checks, pass it through to the completer.
	showAll := false
	filtered := args
	for i, a := range filtered {
		if a == "--all" {
			showAll = true
			filtered = slices.Delete(filtered, i, i+1)
			break
		}
	}

	switch name {
	case commandChat:
		switch len(filtered) {
		case 0, 1:
			return h.completeWithDialoguesIterator(ctx, showAll)
		case 2:
			return h.completeWithModelsIterator(ctx)
		default:
			return iterator.FromSlice[string](nil), nil
		}
	case commandModel:
		return h.completeWithModelsIterator(ctx)
	case commandCompact:
		if len(filtered) > 1 {
			return iterator.FromSlice[string](nil), nil
		}
		return h.completeWithModelsIterator(ctx)
	case commandEffort:
		levels := make([]string, len(validEffortLevels))
		for i, l := range validEffortLevels {
			levels[i] = string(l)
		}
		return iterator.FromSlice(levels), nil
	case commandSkill:
		skills := h.skillRegistry.List()
		names := make([]string, len(skills))
		for i, s := range skills {
			names[i] = s.Name
		}
		return iterator.FromSlice(names), nil
	case commandAddSymbol:
		return referencedSymbols(ctx, h.parser)
	default:
		return iterator.FromSlice[string](nil), nil
	}
}

func (h *aiEditorHandler) Close() error {
	closing := h.exit.CompareAndSwap(0, 1)
	if !closing {
		return nil
	}
	if h.sessionMgr != nil {
		_ = h.sessionMgr.Close()
	}
	h.cancelCtx()
	return h.mcpManager.Close()
}

// newDialogueComponent builds the transient dialogue used by the `?`
// query popup. The popup sizes itself to its content and has no model
// switching of its own, so it renders without a status bar.
func (h *aiEditorHandler) newDialogueComponent(mu sync.Locker) *dialoguetui.Component {
	cfg := h.dialogueConfig(mu)
	cfg.StatusBar.Enabled = false
	return dialoguetui.NewComponent(cfg)
}

func (h *aiEditorHandler) newAgentShell() textapi.REPLHandler {
	opts := []agentshell.Option{
		agentshell.WithMCPInfo(h.mcpManager),
		agentshell.WithHistorySystemPrompt(true),
		agentshell.WithEffort(h.getDefaultEffort, h.setDefaultEffort),
		agentshell.WithMaxTokens(h.getDefaultMaxTokens, h.setDefaultMaxTokens),
		agentshell.WithDreamModel(dreamModelAlias),
	}
	if h.auditStore != nil {
		opts = append(opts, agentshell.WithAuditStore(h.auditStore))
	}
	return agentshell.New(
		h.wm,
		h.llmSvc,
		h.defaultModel,
		h.dialogueStore, h.toolRegistry, h.agentsConfig,
		h.config,
		h.skillRegistry, h.cwd, h.fs,
		h.db, h.exec, h.lsp, h.parser, h.n,
		h.memoryDataPath,
		opts...,
	)
}

func (h *aiEditorHandler) handleChat(cmd textapi.Command) error {
	c, err := h.newChat(cmd, workspaceapi.URI{})
	if err != nil {
		return err
	}
	tab, err := openChatTab(h.wm, c.uri, c.id, c.content)
	if err != nil {
		_ = c.content.Close()
		return err
	}
	if err := h.wm.SetWindowContent(cmd.Window, tab); err != nil {
		return fmt.Errorf("window set content: %v", err)
	}
	return nil
}

// chat is an open agent chat: its content, which owns the chat and ends it
// when closed, and the identity it goes by as a tab.
type chat struct {
	content browserapi.Handler
	id      string
	uri     workspaceapi.URI
}

// newChat opens the chat cmd names, or a new one. The chat is known as uri,
// or as the URI derived from the dialogue and its model when uri is zero.
func (h *aiEditorHandler) newChat(
	cmd textapi.Command, uri workspaceapi.URI,
) (*chat, error) {
	cmd.Args = filterAllFlag(cmd.Args)
	if len(cmd.Args) > 0 {
		if _, err := llmarg.Resolve(h.ctx, h.llmSvc, cmd.Args[0]); err == nil {
			return nil, errors.New("model must be passed as a second argument to a dialogue ID, " +
				"check command manual for more details")
		}
	}
	model := h.defaultModel
	if len(cmd.Args) > 1 {
		model = cmd.Args[1]
		if _, err := llmarg.Resolve(h.ctx, h.llmSvc, model); err != nil {
			return nil, err
		}
	}

	backendService, chatEntry, err := h.modelService(h.ctx, model)
	if err != nil {
		return nil, fmt.Errorf("new backend: %v", err)
	}

	mu := new(sync.Mutex)
	ctx, cancel := context.WithCancel(h.ctx)

	d, err := h.getDialogue(ctx, h.dialogueStore, cmd)
	if err != nil {
		cancel()
		return nil, err
	}
	if _, loaded := h.openChats.LoadOrStore(d.ID, struct{}{}); loaded {
		cancel()
		return nil, fmt.Errorf("agent chat %q is already open", d.ID)
	}
	opened := false
	defer func() {
		if !opened {
			cancel()
			h.openChatAgents.Delete(d.ID)
			h.openChatTx.Delete(d.ID)
			h.openChats.Delete(d.ID)
		}
	}()

	// Create per-session spawner for sub-agent support.
	sessionKey := d.ID
	agentID := "default"
	serviceFactory := func(
		model string,
	) (llmapi.Service, llmapi.ModelEntry, error) {
		return h.modelService(h.ctx, model)
	}
	// Build a separate agentshell for the command adapter. It only needs
	// the base tools for display (e.g. /tools); it carries no mutable state.
	baseTools := h.tools()
	cmdRegistry := agent.NewRegistry(baseTools...)
	// The chat's /compact runs through cmdShell, so its summary budget must
	// follow the chat's /max_tokens override. chatAgent is assigned below,
	// before the chat can run any command.
	var chatAgent *agent.Agent
	cmdShellOpts := []agentshell.Option{
		agentshell.WithMCPInfo(h.mcpManager),
		agentshell.WithMaxTokens(func() int { return chatAgent.MaxOutputTokens() }, nil),
	}
	if h.auditStore != nil {
		cmdShellOpts = append(cmdShellOpts, agentshell.WithAuditStore(h.auditStore))
	}
	cmdShell := agentshell.New(
		h.wm, backendService,
		model,
		h.dialogueStore, cmdRegistry, h.agentsConfig,
		h.config,
		h.skillRegistry, h.cwd, h.fs,
		h.db, h.exec, h.lsp, h.parser, h.n,
		h.memoryDataPath,
		cmdShellOpts...,
	)

	// Build command adapter with session-aware overrides.
	var comp *dialoguetui.Component
	adapter := newCommandAdapter(commandAdapterDeps{
		handler:       cmdShell,
		dialogueID:    d.ID,
		wm:            h.wm,
		store:         h.dialogueStore,
		llmSvc:        h.llmSvc,
		config:        h.config,
		ctx:           h.ctx,
		storage:       h.db,
		currentModel:  model,
		skillRegistry: h.skillRegistry,
		auditStore:    h.auditStore,
		editor:        h.cfg.Editor,
		editorModal:   h.cfg.EditorModal,
		parser:        h.parser,
		fs:            h.fs,
		cwd:           h.cwd,
		git:           h.git,
		reviewContext: h.reviewContextLines,
		mu:            mu,
		comp:          &comp,
		interrupter:   h.p,
	})

	if uri == (workspaceapi.URI{}) {
		uri, err = getModelUri(d.ID, model)
		if err != nil {
			return nil, err
		}
	}
	comp = dialoguetui.NewComponent(h.dialogueConfig(mu))
	syncComp := syncComponent{mu: mu, comp: comp, h: h, tab: newTabStatusReporter(h.wm, uri, h.n)}

	// Replay dialogue history.
	pendingTools := make(map[string]llmapi.ToolCall)
	for _, msg := range d.Messages {
		addMessage(comp, msg, pendingTools)
	}

	dhandler, tx, rx := dialoguetui.Handler(ctx, mu, comp, h.p,
		dialoguetui.WithCommands(adapter),
		dialoguetui.WithContextCompleter(newContextCompleter(h.fs, h.parser)),
		dialoguetui.WithAttachmentOpener(attachmentPreviewer{h}),
		dialoguetui.WithToolCallOpener(newToolCallPreviewer(h)),
		dialoguetui.WithCloseFunc(cancel),
	)

	// Build the agent registry now that tx is available for ask_user.
	prompter := &tuiPrompter{tx: tx, chat: syncComp}
	askUser := agentools.NewAskUser(prompter)
	requestSkill := agentools.NewRequestSkill(prompter)
	exitPlan := agentools.NewExitPlan(h.plansDir, prompter)
	if h.generatePlanPath != nil {
		exitPlan.GeneratePlanPath = h.generatePlanPath
	}
	memRecaller := memory.NewGuardedRecaller(
		memory.NewRecaller(h.fs, h.executor, h.memoryPath), h.config,
	)
	spawner := agent.NewGoroutineSpawner(
		h.dialogueStore, serviceFactory,
		h.agentsConfig,
		h.skillRegistry,
		memRecaller,
		h.projectInstructions,
		sessionKey, agentID, h.cwd,
		prompter,
	)
	spawner.GenerateDialogueID = h.generateDialogueID
	childEvents := make(chan agent.ChildEvent, 64)
	sessionTools := agentools.SessionTools(spawner, spawner.ListAgents(), childEvents, h.skillRegistry)
	skillTool := agentools.NewSkillTool(h.skillRegistry, spawner, childEvents)
	taskStore := taskstore.New()
	progressUpdater := newTUIProgressUpdater(tx, syncComp.tab)
	taskTools := agentools.NewTaskTools(taskStore, progressUpdater)
	allTools := make([]agent.Tool, 0, len(baseTools)+len(sessionTools)+len(taskTools)+5)
	allTools = append(allTools, baseTools...)
	allTools = append(allTools, sessionTools...)
	allTools = append(allTools, askUser)
	allTools = append(allTools, requestSkill)
	allTools = append(allTools, exitPlan)
	allTools = append(allTools, skillTool) // overrides nil-spawner skill tool from baseTools
	allTools = append(allTools, taskTools...)
	webBrowser := h.webBrowser(d.ID)
	if webBrowser != nil {
		allTools = append(allTools, webBrowser)
	}
	chatRegistry := agent.NewRegistry(allTools...)
	chatRegistry.AddOverrides(h.toolRegistry.Overrides())
	adapter.modelToolsFn = func(svc llmapi.Service, model llmapi.ModelEntry) {
		h.registerImageGen(chatRegistry, svc, model)
	}
	adapter.modelToolsFn(backendService, chatEntry)
	chatRegistry.RegisterOverrides("openai",
		agentools.NewUpdatePlan(progressUpdater),
		agentools.NewRequestUserInput(prompter),
	)
	chatRegistry.RegisterOverrides("codex",
		agentools.NewUpdatePlan(progressUpdater),
		agentools.NewRequestUserInput(prompter),
	)
	chatRegistry.RegisterExclusions("openai",
		"TaskCreate", "TaskUpdate", "TaskGet", "TaskList", "ask_user_question",
	)
	chatRegistry.RegisterExclusions("codex",
		"TaskCreate", "TaskUpdate", "TaskGet", "TaskList", "ask_user_question",
	)
	spawner.SetRegistry(chatRegistry)
	spawner.SetHooks(h.hookRunner)
	spawner.SetAttribution(h.attribution)

	h.openChats.Store(d.ID, syncComp)

	chatAgent = agent.NewAgent(
		backendService, chatRegistry, h.skillRegistry,
		h.dialogueStore, memRecaller, agent.Config{
			MaxToolOutputBytes:  h.maxToolOutputBytes,
			AutoCompactRatio:    h.autoCompactRatio,
			CompactSvc:          h.compactSvc,
			SystemPrompt:        h.systemPrompt + agent.ProviderToolAddendum(chatEntry.Provider),
			Attribution:         h.attribution,
			ProjectInstructions: h.projectInstructions,
			SessionKey:          sessionKey,
			AgentID:             agentID,
			Model:               chatEntry,
			Workspace:           h.cwd,
			Hooks:               h.hookRunner,
			Prompter:            prompter,
		},
	)
	if effort := h.getDefaultEffort(); effort != "" {
		chatAgent.SetEffort(effort)
	}
	if maxTokens := h.getDefaultMaxTokens(); maxTokens > 0 {
		chatAgent.SetMaxOutputTokens(maxTokens)
	}
	adapter.agent = chatAgent
	syncComp.setStatusBarState(func(s *dialoguetui.StatusBarState) {
		s.Conversation = d.ID
	})
	adapter.syncStatusBarModel()
	// Token counting walks the whole replayed history, so keep it off
	// the path that opens the tab.
	go debug.CapturePanicReport(func() {
		syncComp.seedContextTokens(h.llmSvc, chatAgent.ModelEntry(), d.Messages)
		_ = h.p.Interrupt(ctx)
	})
	h.openChatAgents.Store(d.ID, chatAgent)
	h.openChatTx.Store(d.ID, tx)
	h.subscribeTools(d.ID, len(baseTools), chatRegistry, cmdRegistry)
	h.warnPendingMCPServers(tx)

	handler, msgRx := h.wrapDialogueHandler(ctx, syncComp, dhandler, rx, d.ID)
	go debug.CapturePanicReport(func() {
		createAgentCompletions(ctx, cancel, tx, msgRx, chatAgent, spawner, childEvents, h.skillRegistry, d.ID, syncComp,
			makeOnCompacted(h.dialogueStore, adapter.compactFn),
			h.dialogueStore)
	})

	bhandler := browserapi.FuncHandler(handler, func() error {
		cancel()
		mu.Lock()
		_ = comp.Close()
		mu.Unlock()
		if webBrowser != nil {
			webBrowser.Close()
		}
		h.unsubscribeTools(d.ID)
		h.openChatAgents.Delete(d.ID)
		h.openChatTx.Delete(d.ID)
		h.openChats.Delete(d.ID)
		// SessionEnd hook (reason=tab_close): fire-and-forget.
		// Purely observational; no result fields are honored.
		h.hookRunner.Run(h.ctx, hooks.Payload{
			SessionID:     d.ID,
			Cwd:           h.cwd,
			HookEventName: hooks.EventSessionEnd,
			Reason:        "tab_close",
		})
		return nil
	})
	opened = true
	return &chat{content: bhandler, id: d.ID, uri: uri}, nil
}

// openChatTab creates the rune-agent chat tab. The visible tab label is the
// dialogue's petname ID (e.g. "rolling-fox"), not the internal
// "rune-agent://<model>/<id>" URI, which would not be useful to users. The
// URI argument remains the tab's unique identity.
func openChatTab(
	wm browserapi.WindowManager, uri workspaceapi.URI, dialogueID string,
	h browserapi.Handler,
) (browserapi.Handler, error) {
	const icon = '󱫆'
	tab, err := wm.Tab(uri, icon, dialogueID, h)
	if err != nil {
		return nil, fmt.Errorf("create tab: %v", err)
	}
	return tab, nil
}

// webBrowser returns the web_browser tool for the chat dialogueID, or nil
// when the workspace host cannot run agent-browser. It looks agent-browser
// up on every call so installing it takes effect in the next chat.
func (h *aiEditorHandler) webBrowser(dialogueID string) *agentools.WebBrowser {
	if h.browserDir == "" {
		return nil
	}
	bin, err := agentools.LookupAgentBrowser(h.ctx, h.executor)
	if err != nil {
		slog.Debug("web_browser tool unavailable", "error", err)
		return nil
	}
	return agentools.NewWebBrowser(h.executor, h.fs, bin, h.browserDir, dialogueID)
}

func (h *aiEditorHandler) registerImageGen(registry *agent.Registry, svc llmapi.Service, model llmapi.ModelEntry) {
	registry.RegisterExclusions(model.Provider, "imagegen")
	if tool := h.imageGen(svc, model); tool != nil {
		registry.RegisterOverrides(model.Provider, tool)
	}
}

func (h *aiEditorHandler) imageGen(svc llmapi.Service, model llmapi.ModelEntry) *agentools.ImageGen {
	if svc == nil || h.imagesDir == "" || h.fs == nil {
		return nil
	}
	available, err := svc.CanGenerateImages(h.ctx, model)
	if err != nil {
		slog.Debug("imagegen tool unavailable", "error", err)
		return nil
	}
	if !available {
		return nil
	}
	return agentools.NewImageGen(svc, model, h.fs, h.imagesDir)
}

// installRoot resolves paths under the data directory of the workspace
// host, which the IDE resolves for extensions (extensionapi.Workspace).
type installRoot interface {
	FindInstalledResource(ctx context.Context, relpath string) (string, error)
}

// webBrowserDir returns the directory for web_browser's files on the
// workspace host, where agent-browser runs: the extension's own data
// directory is a path on the IDE host, which is not the workspace host
// for a remote workspace.
func webBrowserDir(ctx context.Context, root installRoot) (string, error) {
	dataDir, err := root.FindInstalledResource(ctx, ".")
	if err != nil {
		return "", fmt.Errorf("resolve the data directory on the workspace host: %w", err)
	}
	return path.Join(dataDir, "agent-browser"), nil
}

func imageGenDir(ctx context.Context, root installRoot) (string, error) {
	dataDir, err := root.FindInstalledResource(ctx, ".")
	if err != nil {
		return "", fmt.Errorf("resolve the data directory on the workspace host: %w", err)
	}
	return path.Join(dataDir, "generated_images"), nil
}

func getModelUri(id, model string) (workspaceapi.URI, error) {
	model = strings.ReplaceAll(model, "/", "_") // i.e. hf.co/org/model
	model = strings.ReplaceAll(model, ":", "_") // i.e. llama4:scout
	model = url.PathEscape(model)
	uriStr := fmt.Sprintf("rune-agent://%s/%s", model, id)
	return workspaceapi.ParseURI(uriStr)
}

func (h *aiEditorHandler) handleQuery(cmd textapi.Command) error {
	mu := new(sync.Mutex)
	comp := h.newDialogueComponent(mu)
	queryID := strconv.Itoa(rand.Int())
	ctx, cancel := context.WithCancel(h.ctx)
	dhandler, tx, rx := dialoguetui.Handler(ctx, mu, comp, h.p)

	query := strings.Join(cmd.Args, " ")
	if query == "" {
		query = " " // avoid library omiting Content field when zero-valued
	}
	msg := llmapi.Message{Content: query, Role: llmapi.RoleUser}
	addMessage(comp, msg, nil)

	syncComp := syncComponent{mu: mu, comp: comp, h: h, tab: newTabStatusReporter(h.wm, workspaceapi.URI{}, h.n)}

	qrx := make(chan dialoguetui.SubmitMessage)
	handler, msgRx := h.wrapDialogueHandler(ctx, syncComp, dhandler, qrx, "")

	// wrap rx to enable sending query and so get
	// context cancelation for free
	go debug.CapturePanicReport(
		// do not store queries in store after user is done
		func() {

			defer h.dialogueStore.Delete(ctx, queryID) //nolint:errcheck

			select {
			case qrx <- dialoguetui.SubmitMessage{Text: query}:
			case <-ctx.Done():
				return
			}
			for {
				select {
				case msg := <-rx:
					select {
					case qrx <- msg:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}

		})

	serviceFactory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
		return h.modelService(h.ctx, model)
	}
	prompter := &tuiPrompter{tx: tx, chat: syncComp}
	spawner := agent.NewGoroutineSpawner(
		h.dialogueStore, serviceFactory,
		h.agentsConfig,
		h.skillRegistry,
		agent.NoMemory(),
		h.projectInstructions,
		queryID, "query", h.cwd,
		prompter,
	)
	spawner.SetRegistry(agent.NewRegistry(h.tools()...))
	spawner.SetAttribution(h.attribution)
	spawner.GenerateDialogueID = h.generateDialogueID
	childEvents := make(chan agent.ChildEvent, 64)

	go debug.CapturePanicReport(func() {
		createAgentCompletions(ctx, cancel, tx, msgRx,
			h.queryAgent, spawner, childEvents, h.skillRegistry, queryID, syncComp, nil, h.dialogueStore)
	})

	bhandler := browserapi.FuncHandler(handler, func() error {
		cancel()
		mu.Lock()
		defer mu.Unlock()
		return comp.Close()
	})
	floating := browserapi.FuncFloating(bhandler, func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		const width = 100
		return width, comp.Height(width)
	})
	floatingConfig := browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}
	if _, err := h.wm.Floating(floating, floatingConfig); err != nil {
		return fmt.Errorf("floating window: %v", err)
	}
	return nil
}

func (h *aiEditorHandler) getDialogue(
	ctx context.Context, dialogueStore dialoguemanager.Store, cmd textapi.Command,
) (dialoguemanager.Dialogue, error) {
	dialogueID := parseDialogueID(cmd)
	if dialogueID == "" {
		// No user-provided ID — generate a unique one.
		dialogueID = dialoguemanager.GenerateUniqueID(ctx, dialogueStore, "")
	}
	d, err := dialogueStore.Get(ctx, dialogueID)
	if err != nil {
		if !errors.Is(err, storageapi.ErrNotFound) {
			return dialoguemanager.Dialogue{}, fmt.Errorf("get dialogue from store: %w", err)
		}
		d.ID = dialogueID
	}
	return d, nil
}

func (h *aiEditorHandler) completeWithDialoguesIterator(ctx context.Context, showAll bool) (
	iterator.Iterator[string], error,
) {
	listIt, err := h.dialogueStore.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("dialogue store list: %w", err)
	}

	all, err := iterator.ToSlice(ctx, listIt)
	if err != nil {
		return nil, fmt.Errorf("dialogue store list collect: %w", err)
	}

	dialogueTier := func(d dialoguemanager.DialogueHeader) int {
		ws, hasWS := d.Workspace()
		if hasWS && ws.Equal(h.cwd) {
			return 0
		}
		if hasWS && h.gitID.worktrees[ws.String()] != "" {
			return 1
		}
		if !hasWS {
			return 2
		}
		return 3
	}

	// Filter: drop empty IDs, sub-agents, and (unless showAll)
	// foreign-workspace dialogues.
	filtered := all[:0]
	for _, d := range all {
		if d.ID == "" || d.SubAgent {
			continue
		}
		if !showAll && h.isForeignDialogue(d) {
			continue
		}
		filtered = append(filtered, d)
	}

	// Current-workspace dialogues stay pinned above newer sibling worktree
	// dialogues so completions prefer the user's active context.
	sort.SliceStable(filtered, func(i, j int) bool {
		ti, tj := dialogueTier(filtered[i]), dialogueTier(filtered[j])
		if ti != tj {
			return ti < tj
		}
		return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
	})

	// Map to display strings.
	out := make([]string, len(filtered))
	for i, d := range filtered {
		ws, hasWS := d.Workspace()
		switch {
		case !hasWS || ws.Equal(h.cwd):
			// Legacy dialogues are tagged so the user can tell they
			// have no workspace association.
			if !hasWS {
				out[i] = "<legacy>:" + d.ID
			} else {
				out[i] = d.ID
			}
		case h.gitID.worktrees[ws.String()] != "":
			out[i] = h.gitID.worktrees[ws.String()] + ":" + d.ID
		default:
			out[i] = ws.String() + ":" + d.ID
		}
	}
	return iterator.FromSlice(out), nil
}

// isForeignDialogue reports whether d belongs to a workspace that is
// neither this one nor one of its sibling worktrees.
func (h *aiEditorHandler) isForeignDialogue(d dialoguemanager.DialogueHeader) bool {
	ws, hasWS := d.Workspace()
	return hasWS && !ws.Equal(h.cwd) && h.gitID.worktrees[ws.String()] == ""
}

func (h *aiEditorHandler) completeWithModelsIterator(ctx context.Context) (
	iterator.Iterator[string], error,
) {
	return iterator.Map(h.llmSvc.Models(), func(e llmapi.ModelEntry) string {
		return e.Provider + "/" + e.Name
	}), nil
}

func (h *aiEditorHandler) wrapDialogueHandler(
	ctx context.Context, comp syncComponent,
	dhandler tui.Handler, rx <-chan dialoguetui.SubmitMessage,
	dialogueID string,
) (tui.Handler, <-chan completionRequest) {
	ret := make(chan completionRequest)

	// wrap dialogue.Handler's rx chan to add adhoc
	// cancelation of completion requests
	var cancel func()
	mu := new(sync.Mutex)

	go debug.CapturePanicReport(func() {

		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-rx:
				reqCtx, cancelFn := context.WithCancel(ctx)
				mu.Lock()
				cancel = cancelFn
				mu.Unlock()
				finalized := finalizeSubmitMessage(msg)
				parts := h.attachmentContentParts(reqCtx, finalized.Attachments)
				select {
				case ret <- completionRequest{
					displayText: finalized.DisplayText,
					modelText:   finalized.ModelText,
					skillName:   msg.SkillName,
					attachments: parts,
					ctx:         reqCtx,
				}:
				case <-ctx.Done():
					return
				}
			}
		}

	})

	// wrap it for ctrl-c cancelation of context
	return handler.Wrap(dhandler, func(ev term.Event) (exit bool, handled bool) {
		// Handle only runs while this chat has browser focus, so it is
		// the signal workspace commands use to find the target chat.
		h.recordFocusedChat(dialogueID)
		if ev.Ch == 'c' && ev.Mod == term.ModCtrl {
			// An open '#' completion band owns Ctrl-C: it dismisses the
			// band without cancelling the in-flight completion request.
			completing := comp.completionOpen()
			// Let the dialogue handler dismiss any active prompt first.
			// Otherwise the prompt UI would stay visible while only the
			// underlying completion context is cancelled.
			exit, _ = dhandler.Handle(ev)
			if completing {
				return exit, true
			}
			mu.Lock()
			cancelFn := cancel
			cancel = nil
			mu.Unlock()
			if cancelFn != nil {
				cancelFn()
				_, _ = comp.h.n.Notify(browserapi.LevelInfo, "canceled completion request")
			}
			handled = true
			return
		}
		return dhandler.Handle(ev)
	}), ret
}

// parseDialogueID extracts a user-provided dialogue ID from the
// command args. Returns empty string when no ID was provided,
// signaling that a new unique ID should be generated.
func parseDialogueID(cmd textapi.Command) string {
	var id string
	for _, arg := range cmd.Args {
		if arg == "--all" || arg == "" {
			continue
		}
		id = arg
		break
	}
	if id == "" {
		return ""
	}
	// Strip workspace prefix (e.g. "worktree-name:myid" → "myid").
	if i := strings.IndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// filterAllFlag returns args with "--all" removed.
func filterAllFlag(args []string) (out []string) {
	for _, a := range args {
		if a != "--all" {
			out = append(out, a)
		}
	}
	return out
}

// baseDialogueID strips the archive suffix from an archived dialogue ID.
// Handles both "foo-archived" and "foo-archived-2" formats produced by NextArchivedID.
func baseDialogueID(archivedID string) string {
	if i := strings.LastIndex(archivedID, "-archived"); i >= 0 {
		return archivedID[:i]
	}
	return archivedID
}

func replayMessages(d dialoguemanager.Dialogue) []llmapi.Message {
	// New dialogues persist the approved plan as a RoleUser anchor message
	// directly inside d.Messages (see clearContext / CompactDialogue), so we
	// must NOT re-inject when it is already present — that would duplicate
	// the plan in the TUI and in any path that feeds the LLM.
	//
	// Older dialogues (persisted before the fix) only kept the plan as
	// out-of-band metadata. For those we still inject a synthetic user
	// message after the system prompt so the conversation has a valid
	// user/assistant alternation.
	if d.ApprovedPlan == nil {
		return d.Messages
	}
	planMsg := llmapi.Message{
		Role:    llmapi.RoleUser,
		Content: fmt.Sprintf("Plan approved. Saved to %s\n\n%s", d.ApprovedPlan.Path, d.ApprovedPlan.Body),
	}
	if len(d.Messages) >= 2 &&
		d.Messages[1].Role == llmapi.RoleUser &&
		d.Messages[1].Content == planMsg.Content {
		return d.Messages
	}
	msgs := make([]llmapi.Message, 0, len(d.Messages)+1)
	if len(d.Messages) > 0 {
		msgs = append(msgs, d.Messages[0])
	}
	msgs = append(msgs, planMsg)
	if len(d.Messages) > 1 {
		msgs = append(msgs, d.Messages[1:]...)
	}
	return msgs
}

func addMessage(c *dialoguetui.Component, msg llmapi.Message, pendingTools map[string]llmapi.ToolCall) {
	switch msg.Role {
	case llmapi.RoleAssistant:
		if msg.ReasoningContent != "" {
			c.AddReasoningChunk(msg.ReasoningContent)
		}
		if msg.Content != "" {
			c.AddReceiveMessageChunk(msg.Content)
			c.AddReceiveMessageBreak()
		}
		for _, call := range msg.ToolCalls {
			if pendingTools != nil {
				pendingTools[call.ID] = call
			}
		}
	case llmapi.RoleUser:
		attachments := replayedAttachments(msg)
		// Tool images are carried in synthetic user messages for providers
		// that cannot accept images in tool results. They are not user turns.
		if msg.Content == "" && len(attachments) == 0 && slices.ContainsFunc(msg.MultiContent, func(part llmapi.ContentPart) bool {
			return part.Type == llmapi.ContentPartTypeImageURL
		}) {
			return
		}
		if replayed, ok := parseStoredCommandMessage(msg.Content); ok {
			c.AddSendMessage(replayed)
		} else if strings.HasPrefix(msg.Content, agent.CompactSummaryPrefix) ||
			strings.HasPrefix(msg.Content, "Plan approved. Saved to ") {
			c.AddSendMessageMarkdown(msg.Content)
		} else {
			c.AddSendMessageAttachments(msg.Content, attachments)
		}
	case llmapi.RoleSystem:
	case llmapi.RoleTool:
		id := msg.ToolCallID
		name := cmp.Or(msg.Name, "tool")
		args := ""
		if pendingTools != nil {
			if call, ok := pendingTools[id]; ok {
				name = call.Function.Name
				args = call.Function.Arguments
				delete(pendingTools, id)
			}
		}
		if name == "update_plan" {
			var parsed struct {
				Plan []struct {
					Step   string `json:"step"`
					Status string `json:"status"`
				} `json:"plan"`
			}
			if err := json.Unmarshal([]byte(args), &parsed); err == nil {
				for i, step := range parsed.Plan {
					c.UpdateTaskProgress(dialoguetui.ProgressTaskEntry{
						ID:      fmt.Sprintf("plan:%d", i),
						Subject: step.Step,
						Status:  step.Status,
					})
				}
				for i := len(parsed.Plan); i < 32; i++ {
					c.UpdateTaskProgress(dialoguetui.ProgressTaskEntry{
						ID:     fmt.Sprintf("plan:%d", i),
						Status: "deleted",
					})
				}
				return
			}
		}
		summary := ""
		if name == "imagegen" {
			var parsed struct {
				Prompt string `json:"prompt"`
			}
			if json.Unmarshal([]byte(args), &parsed) == nil {
				summary = parsed.Prompt
			}
		}
		c.AddToolCall(id, name, args, summary)
		c.CompleteToolCall(id, name, args, summary, msg.Content, false)
		for _, image := range toolResultImages(name, msg.Content, false) {
			c.AddToolCallImage(id, image, summary)
		}
	}
}

func toolResultImages(name, output string, isError bool) []string {
	if name == "imagegen" && !isError {
		if image, ok := agentools.GeneratedImagePath(output); ok {
			return []string{image}
		}
	}
	return nil
}

func firstModel(svc llmapi.Service) string {
	it := svc.Models()
	defer func() { _ = it.Close() }()
	e, ok := it.Next(context.Background())
	if !ok {
		return ""
	}
	return e.Name
}

type completionRequest struct {
	// displayText is the text the user composed, unchanged. It is the
	// human-readable form of the turn: no attachment links are rewritten.
	displayText string
	// modelText is displayText with every valid inline attachment link
	// rewritten to its "<attachment-N>" placeholder, matching the IDs on
	// attachments below. This is the text actually sent to the model.
	//
	// TODO(agent-split): ag.Run below persists whatever text it is given
	// as the turn's message content, so replay currently shows modelText
	// rather than displayText. The agent package split should persist
	// displayText for the turn and keep modelText+attachments as what is
	// actually sent to the provider.
	modelText string
	skillName string
	ctx       context.Context
	// attachments are canonical v1 content parts for files the user
	// attached to the chat, sent alongside modelText in the same user
	// message.
	attachments []llmapi.ContentPart
}

const truncatedTurnHint = "At high and max/xhigh effort levels, models may think more extensively and can be more likely to exhaust the max_tokens budget. Consider increasing max_tokens to give the model more room (/max_tokens 64000), or lowering the effort level (/effort medium)."

type turnOutcome uint8

const (
	turnOutcomeNone turnOutcome = iota
	turnOutcomeCompleted
	turnOutcomeTruncated
	turnOutcomeCanceled
	turnOutcomeError
)

// reportTurnOutcome reports how the turn ended as the chat's status:
// done, summarized by the reply; failed, with the reason; or withdrawn
// when the user canceled it.
func reportTurnOutcome(tab *tabStatusReporter, outcome turnOutcome, reason, reply string) {
	switch outcome {
	case turnOutcomeCompleted:
		msg := replySummary(reply)
		if msg == "" {
			msg = statusMsgCompleted
		}
		tab.done(msg)
	case turnOutcomeTruncated:
		tab.truncated()
	case turnOutcomeCanceled:
		tab.canceled()
	case turnOutcomeError:
		if reason == "" {
			tab.failed(statusMsgFailed)
		} else {
			tab.failed(fmt.Sprintf("%s: %s", statusMsgFailed, reason))
		}
	}
}

// spawnsSubAgent reports whether the tool named name may run a
// sub-agent whose events arrive as agent.ChildEvents of its call.
func spawnsSubAgent(name string) bool {
	return name == "agent" || name == "skill"
}

func outcomeAndReasonForFinishReason(reason llmapi.FinishReason) (turnOutcome, string) {
	switch reason {
	case llmapi.FinishReasonStop:
		return turnOutcomeCompleted, ""
	case llmapi.FinishReasonLength:
		return turnOutcomeTruncated, ""
	case llmapi.FinishReasonToolCall:
		return turnOutcomeError, "the model stopped while requesting tool calls"
	case llmapi.FinishReasonContentFilter:
		return turnOutcomeError, "the response was blocked by a content filter"
	case llmapi.FinishReasonRefusal:
		return turnOutcomeError, "the model declined to continue"
	case llmapi.FinishReasonNull:
		return turnOutcomeError, "the response ended unexpectedly"
	default:
		return turnOutcomeError, fmt.Sprintf("unexpected finish reason: %s", reason)
	}
}

// makeOnCompacted returns a callback that fetches the compacted dialogue
// from the store and replays it in the TUI via compactFn. It is used by
// createAgentCompletions to handle EventCompacted from both the main
// agent loop and child (sub-agent) events.
func makeOnCompacted(store dialoguemanager.Store, compactFn func([]llmapi.Message)) func(string) {
	return func(dialogueID string) {
		compacted, err := store.Get(context.Background(), dialogueID)
		if err != nil {
			slog.Error("compacted: fetch dialogue", "id", dialogueID, "error", err)
			return
		}
		compactFn(replayMessages(compacted))
	}
}

// parentDialogueContextMessages loads the parent dialogue identified by
// dialogueID and returns the subset of its stored messages suitable for
// seeding a sub-agent whose skill opts into parent-context sharing.
// The returned slice:
//   - excludes messages with role system (the parent system prompt and
//     any transient system injections are not relevant to the sub-agent),
//   - excludes assistant tool-call messages and tool-result messages
//     (the child sub-agent has its own tool set, so forwarding parent
//     tool calls would produce dangling references),
//   - excludes an exact match of currentMessage appearing at the tail
//     (so the slash-command envelope message is not duplicated), and
//   - returns freshly copied llmapi.Message values.
//
// store MUST NOT be nil; passing a nil store is a developer error.
// Returns nil when the dialogue has not been persisted yet or on error.
func parentDialogueContextMessages(
	ctx context.Context, store dialoguemanager.Store,
	dialogueID, currentMessage string,
) []llmapi.Message {
	if store == nil {
		panic("parentDialogueContextMessages: store must not be nil")
	}
	if dialogueID == "" {
		return nil
	}
	d, err := store.Get(ctx, dialogueID)
	if err != nil {
		// Absent parent dialogue (not yet persisted) is expected on the
		// first turn; no context to forward.
		return nil
	}
	msgs := d.Messages
	// Drop a trailing occurrence of the current slash-command envelope
	// message if it has already been appended to the parent dialogue.
	if n := len(msgs); n > 0 {
		tail := msgs[n-1]
		if tail.Role == llmapi.RoleUser && tail.Content == currentMessage {
			msgs = msgs[:n-1]
		}
	}
	out := make([]llmapi.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == llmapi.RoleSystem || m.Role == llmapi.RoleTool {
			continue
		}
		// Drop assistant messages that exist solely to carry tool
		// calls; tool calls from the parent reference tools that may
		// not exist in the child sub-agent.
		if m.Role == llmapi.RoleAssistant && len(m.ToolCalls) > 0 && m.Content == "" && m.ReasoningContent == "" {
			continue
		}
		// For assistant messages that mix text and tool calls, strip
		// the tool calls and keep the text.
		if len(m.ToolCalls) > 0 {
			m.ToolCalls = nil
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func createAgentCompletions(
	ctx context.Context, cancel func(),
	tx chan<- dialoguetui.MessageEvent, rx <-chan completionRequest,
	ag *agent.Agent, spawner agent.Spawner,
	childEvents <-chan agent.ChildEvent,
	skillRegistry *skills.SkillRegistry,
	id string,
	syncComp syncComponent,
	onCompacted func(dialogueID string),
	parentStore dialoguemanager.Store,
) {
	if parentStore == nil {
		panic("createAgentCompletions: parentStore must not be nil")
	}
	// Forward child events from sub-agents to the TUI.
	// The goroutine must exit before we close tx to avoid
	// sending on a closed channel.
	var childWg sync.WaitGroup
	childWg.Go(func() {
		debug.CapturePanicReport(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case cev, ok := <-childEvents:
					if !ok {
						return
					}
					var msg dialoguetui.MessageEvent
					switch cev.Event.Type {
					case agent.EventToolCall:
						syncComp.tab.childToolCall(cev.ParentToolCallID,
							cev.Event.ToolName, cev.Event.ToolSummary)
						msg = dialoguetui.MessageEvent{
							Type:             dialoguetui.MessageEventToolCall,
							ToolCallID:       cev.Event.ToolCallID,
							ToolName:         cev.Event.ToolName,
							ToolArgs:         cev.Event.ToolArgs,
							ToolSummary:      cev.Event.ToolSummary,
							ToolStartTime:    cev.Event.ToolStartTime,
							ParentToolCallID: cev.ParentToolCallID,
						}
					case agent.EventToolResult:
						msg = dialoguetui.MessageEvent{
							Type:             dialoguetui.MessageEventToolResult,
							ToolCallID:       cev.Event.ToolCallID,
							ToolName:         cev.Event.ToolName,
							ToolArgs:         cev.Event.ToolArgs,
							ToolSummary:      cev.Event.ToolSummary,
							ToolOutput:       cev.Event.ToolOutput,
							IsError:          cev.Event.IsError,
							ToolDuration:     cev.Event.ToolDuration,
							ParentToolCallID: cev.ParentToolCallID,
							ImagePaths:       toolResultImages(cev.Event.ToolName, cev.Event.ToolOutput, cev.Event.IsError),
						}
					case agent.EventToolsDropped:
						msg = dialoguetui.MessageEvent{
							Type:               dialoguetui.MessageEventToolsDropped,
							DroppedToolCallIDs: cev.Event.DroppedToolCallIDs,
						}
					case agent.EventMemoryRecall:
						msg = agentMemoriesToTUI(cev.Event)
					case agent.EventCompacted:
						if cev.Event.ArchivedDialogueID != "" && onCompacted != nil {
							dialogueID := baseDialogueID(cev.Event.ArchivedDialogueID)
							onCompacted(dialogueID)
						}
						continue
					case agent.EventDone:
						msg = dialoguetui.MessageEvent{
							Type:             dialoguetui.MessageEventChildResult,
							ParentToolCallID: cev.ParentToolCallID,
							ToolOutput:       cev.Event.Text,
							IsError:          cev.Event.IsError,
						}
					default:
						continue
					}
					select {
					case tx <- msg:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	})
	defer func() {
		cancel()
		childWg.Wait()
		close(tx)
	}()

	for {
		var req completionRequest
		select {
		case <-ctx.Done():
			return
		case req = <-rx:
		}
		// Signal the dialogue handler that the agent is busy so
		// follow-up messages are queued instead of sent directly.
		select {
		case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy, Busy: true}:
		case <-ctx.Done():
			return
		}
		turnStart := time.Now()
		syncComp.beginTurn(turnStart)
		endTurn := syncComp.endTurn
		setPhase := func(p string) {
			syncComp.setStatusBarState(func(s *dialoguetui.StatusBarState) {
				s.Phase = p
			})
		}
		upsRes := ag.Hooks().Run(req.ctx, hooks.Payload{
			SessionID:     id,
			Cwd:           ag.Workspace(),
			HookEventName: hooks.EventUserPromptSubmit,
			Prompt:        req.displayText,
		})
		if upsRes.Blocked() {
			reason := upsRes.Reason
			if reason == "" {
				reason = "blocked by UserPromptSubmit hook"
			}
			select {
			case tx <- dialoguetui.MessageEvent{
				Type: dialoguetui.MessageEventError, Text: reason,
			}:
			case <-ctx.Done():
			}
			select {
			case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBreak}:
			case <-ctx.Done():
			}
			syncComp.tab.failed(reason)
			endTurn()
			continue
		}

		// Agent-type skills: spawn a sub-agent whose events are
		// rendered as top-level (tool calls, text, reasoning all
		// visible in the TUI).
		var it iterator.Iterator[agent.Event]
		if req.skillName != "" {
			if skill, ok := skillRegistry.Get(req.skillName); ok && skill.Type == "agent" {
				var initialMessages []llmapi.Message
				if skill.ParentContext {
					initialMessages = parentDialogueContextMessages(
						req.ctx, parentStore, id, req.displayText,
					)
				}
				handle, skillErr := spawner.Run(req.ctx, agent.RunRequest{
					Label:             skill.Name,
					Model:             llmarg.Qualify(ag.ModelEntry()),
					Message:           req.modelText,
					DisplayMessage:    req.displayText,
					Attachments:       req.attachments,
					AdditionalContext: upsRes.AdditionalContext,
					Tools:             skillRegistry.AgentPolicy(skill),
					SystemPrompt:      skill.Body,
					InitialMessages:   initialMessages,
				})
				if skillErr != nil {
					select {
					case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventError, Text: skillErr.Error()}:
					case <-ctx.Done():
					}
					select {
					case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBreak}:
					case <-ctx.Done():
					}
					syncComp.tab.failed(fmt.Sprintf("skill %s: %v", skill.Name, skillErr))
					endTurn()
					continue
				}
				it = handle.Events
			}
		}
		if it == nil {
			runOpts := []agent.RunOption{
				agent.WithDisplayMessage(req.displayText),
				agent.WithAdditionalContext(upsRes.AdditionalContext),
			}
			if req.skillName != "" {
				runOpts = append(runOpts, agent.WithSkillName(req.skillName))
			}
			if len(req.attachments) > 0 {
				runOpts = append(runOpts, agent.WithAttachments(req.attachments))
			}
			it = ag.Run(req.ctx, id, req.modelText, runOpts...)
		}
		func() {
			defer it.Close() //nolint:errcheck
			breakSent := false
			eventErrorSent := false
			// Always signal turn completion to the TUI when we
			// exit, even on context cancellation or abnormal agent
			// exit. AddReceiveMessageBreak is idempotent; a
			// duplicate after EventDone is harmless. This ensures
			// the turn's spinner animation stops and any dropped
			// tool-result events are cleaned up by completeRunning.
			defer func() {
				if breakSent {
					return
				}
				select {
				case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBreak}:
				case <-ctx.Done():
				}
			}()
			outcome := turnOutcomeNone
			outcomeReason := ""
			// reply collects the text of the inference in progress, so
			// that the turn's outcome can be summarized by its last one.
			var reply strings.Builder
			defer func() {
				if outcome == turnOutcomeNone {
					switch {
					case errors.Is(ctx.Err(), context.Canceled), errors.Is(req.ctx.Err(), context.Canceled):
						outcome = turnOutcomeCanceled
						outcomeReason = string(llmapi.FinishReasonNull)
					}
				}
				reportTurnOutcome(syncComp.tab, outcome, outcomeReason, reply.String())
			}()
			for {
				ev, ok := it.Next(req.ctx)
				if !ok {
					break
				}
				switch ev.Type {
				case agent.EventMemoryRecall:
					select {
					case tx <- agentMemoriesToTUI(ev):
					case <-ctx.Done():
						return
					}
				case agent.EventInferenceStart:
					setPhase(phaseSending)
					syncComp.tab.setTitle(statusTitleSending)
					reply.Reset()
				case agent.EventInferenceReady:
					setPhase(phaseThinking)
					syncComp.tab.setTitle(statusTitleReasoning)
				case agent.EventFirstContent:
					setPhase(phaseReceiving)
					syncComp.tab.setTitle(statusTitleWriting)
				case agent.EventReasoning:
					if ev.Reasoning == "" {
						continue
					}
					select {
					case tx <- dialoguetui.MessageEvent{
						Type: dialoguetui.MessageEventReasoning,
						Text: ev.Reasoning,
					}:
					case <-ctx.Done():
						return
					}
				case agent.EventText:
					if ev.Text == "" {
						continue
					}
					reply.WriteString(ev.Text)
					select {
					case tx <- dialoguetui.MessageEvent{
						Type: dialoguetui.MessageEventText,
						Text: ev.Text,
					}:
					case <-ctx.Done():
						return
					}
				case agent.EventToolsStart:
					setPhase(phaseToolCalling)
					syncComp.tab.setTitle(statusTitleTools)
				case agent.EventToolCall:
					syncComp.tab.setToolCall(ev.ToolName, ev.ToolSummary)
					if spawnsSubAgent(ev.ToolName) {
						syncComp.tab.childStart(ev.ToolCallID, ev.ToolSummary)
					}
					select {
					case tx <- dialoguetui.MessageEvent{
						Type:          dialoguetui.MessageEventToolCall,
						ToolCallID:    ev.ToolCallID,
						ToolName:      ev.ToolName,
						ToolArgs:      ev.ToolArgs,
						ToolSummary:   ev.ToolSummary,
						ToolStartTime: ev.ToolStartTime,
					}:
					case <-ctx.Done():
						return
					}
				case agent.EventToolResult:
					syncComp.tab.childEnd(ev.ToolCallID)
					select {
					case tx <- dialoguetui.MessageEvent{
						Type:         dialoguetui.MessageEventToolResult,
						ToolCallID:   ev.ToolCallID,
						ToolName:     ev.ToolName,
						ToolArgs:     ev.ToolArgs,
						ToolSummary:  ev.ToolSummary,
						ToolOutput:   ev.ToolOutput,
						IsError:      ev.IsError,
						ToolDuration: ev.ToolDuration,
						ImagePaths:   toolResultImages(ev.ToolName, ev.ToolOutput, ev.IsError),
					}:
					case <-ctx.Done():
						return
					}
				case agent.EventToolsDropped:
					select {
					case tx <- dialoguetui.MessageEvent{
						Type:               dialoguetui.MessageEventToolsDropped,
						DroppedToolCallIDs: ev.DroppedToolCallIDs,
					}:
					case <-ctx.Done():
						return
					}
				case agent.EventRateLimitWarning:
					setPhase(phaseRateLimited)
					if ev.RateLimit != nil {
						syncComp.tab.notify(statusTitleRateLimit, ev.RateLimit.Message)
						select {
						case tx <- dialoguetui.MessageEvent{
							Type: dialoguetui.MessageEventWarning,
							Text: ev.RateLimit.Message,
						}:
						case <-ctx.Done():
							return
						}
					}
				case agent.EventUsageUpdate:
					syncComp.setStatusBarState(func(s *dialoguetui.StatusBarState) {
						s.Usage = ev.Usage
						s.ContextTokens = ev.Context.TokensSent + ev.Context.TokensReceived
						if ev.Context.Window > 0 {
							s.ContextWindow = ev.Context.Window
						}
					})
				case agent.EventCompacting:
					setPhase(phaseCompacting)
					syncComp.tab.setTitle(statusTitleCompacting)
				case agent.EventCompacted:
					setPhase(phaseSending)
					syncComp.tab.setTitle(statusTitleSending)
					if onCompacted != nil {
						onCompacted(id)
					}
					if ev.ArchivedDialogueID != "" {
						select {
						case tx <- dialoguetui.MessageEvent{
							Type: dialoguetui.MessageEventWarning,
							Text: fmt.Sprintf(
								"Conversation compacted. Old conversation stored as **%s**.",
								ev.ArchivedDialogueID,
							),
						}:
						case <-ctx.Done():
							return
						}
					}
				case agent.EventDone:
					outcome, outcomeReason = outcomeAndReasonForFinishReason(ev.FinishReason)
					if ev.FinishReason == llmapi.FinishReasonLength {
						select {
						case tx <- dialoguetui.MessageEvent{
							Type: dialoguetui.MessageEventWarning,
							Text: truncatedTurnHint,
						}:
						case <-ctx.Done():
							return
						}
					}
					select {
					case tx <- dialoguetui.MessageEvent{
						Type: dialoguetui.MessageEventBreak,
					}:
						breakSent = true
					case <-ctx.Done():
						return
					}
				case agent.EventRefusal:
					outcome, outcomeReason = outcomeAndReasonForFinishReason(ev.FinishReason)
					select {
					case tx <- dialoguetui.MessageEvent{
						Type: dialoguetui.MessageEventWarning,
						Text: "The model declined to continue with this request.",
					}:
					case <-ctx.Done():
						return
					}
					select {
					case tx <- dialoguetui.MessageEvent{
						Type: dialoguetui.MessageEventBreak,
					}:
						breakSent = true
					case <-ctx.Done():
						return
					}
				case agent.EventError:
					if ev.Error != nil && !errors.Is(ev.Error, context.Canceled) {
						eventErrorSent = true
						outcome = turnOutcomeError
						outcomeReason = ev.Error.Error()
						errMsg := fmt.Sprintf("agent: %v", ev.Error)
						select {
						case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventError, Text: errMsg}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
			if err := it.Err(); err != nil {
				if !errors.Is(err, context.Canceled) &&
					!(eventErrorSent && strings.HasPrefix(err.Error(), "stream:")) {
					if outcome == turnOutcomeNone || outcome == turnOutcomeCompleted {
						outcome = turnOutcomeError
						outcomeReason = err.Error()
					}
					errMsg := fmt.Sprintf("agent stream: %v", err)
					select {
					case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventError, Text: errMsg}:
					case <-ctx.Done():
						return
					}
				} else if outcome == turnOutcomeNone {
					outcome = turnOutcomeCanceled
					outcomeReason = string(llmapi.FinishReasonNull)
				}
			}
			if outcome == turnOutcomeNone {
				switch {
				case errors.Is(ctx.Err(), context.Canceled), errors.Is(req.ctx.Err(), context.Canceled):
					outcome = turnOutcomeCanceled
					outcomeReason = ""
				default:
					outcome = turnOutcomeError
					outcomeReason = "the response ended unexpectedly"
				}
			}
		}()
		endTurn()
		// Signal that the agent is idle; the handler will drain
		// any queued follow-up messages at this point.
		select {
		case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy, Busy: false}:
		case <-ctx.Done():
			return
		}
	}
}

func readWorkspaceFile(wfs workspaceapi.FileSystem, path string) ([]byte, error) {
	f, err := wfs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// agentMemoriesToTUI converts an agent EventMemoryRecall into a TUI
// MessageEvent, mapping []agent.Memory to []dialoguetui.MemoryRecallEntry.
func agentMemoriesToTUI(ev agent.Event) dialoguetui.MessageEvent {
	entries := make([]dialoguetui.MemoryRecallEntry, len(ev.Memories))
	for i, m := range ev.Memories {
		entries[i] = dialoguetui.MemoryRecallEntry{ID: m.ID, Content: m.Content}
	}
	return dialoguetui.MessageEvent{
		Type:           dialoguetui.MessageEventMemoryRecall,
		Memories:       entries,
		MemoryDuration: ev.MemoryDuration,
	}
}

// resolveInputBackgroundColor returns the compose input background color.
// It keeps def unless input_box_attr.bg is set to an explicit,
// non-default color, letting the shared input_box_attr drive the compose
// background without a dedicated key.
func resolveInputBackgroundColor(def term.Color, pconfig config.Config) term.Color {
	attr, err := config.GetAttributes(pconfig, "input_box_attr")
	if err != nil || attr.Bg == term.ColorDefault {
		return def
	}
	return attr.Bg
}

// resolvePinnedUserMsgMaxLines reads pinned_user_msg_max_lines, the rows
// the pinned user message may take over the top of the transcript. Zero
// disables the pin, and a negative value is clamped to it.
func resolvePinnedUserMsgMaxLines(pconfig config.Config) int {
	v, err := pconfig.GetInt("pinned_user_msg_max_lines")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'pinned_user_msg_max_lines' from config", "error", err)
		}
		return defaultPinnedUserMsgMaxLines
	}
	return max(0, v)
}

// resolveTranscriptHorizontalPadding reads transcript_horizontal_padding,
// the columns left beside the transcript, split evenly on either side. A
// negative value is the transcript's width instead, and the transcript
// fills a tab narrower than that. Zero fills the tab.
func resolveTranscriptHorizontalPadding(pconfig config.Config) int {
	v, err := pconfig.GetInt("transcript_horizontal_padding")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'transcript_horizontal_padding' from config", "error", err)
		}
		return defaultComponentCfg.MessagesRowConfig.PadHorizontal
	}
	return v
}

// resolveUserMsgIcon reads user_msg_icon, the character marking each of
// the user's messages in the chat. An empty string hides the icon. Any
// other value must be a single character, as the icon takes exactly one
// cell; a longer value keeps def rather than being cut short.
func resolveUserMsgIcon(def rune, pconfig config.Config) rune {
	v, err := pconfig.GetString("user_msg_icon")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'user_msg_icon' from config", "error", err)
		}
		return def
	}
	icon := []rune(v)
	switch len(icon) {
	case 0:
		return 0
	case 1:
		return icon[0]
	}
	slog.Warn("'user_msg_icon' must be a single character", "value", v)
	return def
}
