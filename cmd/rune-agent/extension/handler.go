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
	"io"
	"log/slog"
	"maps"
	"math/rand"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
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
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"
)

const (
	commandQuery = "?"
	commandChat  = "agent"

	// chatScheme is the scheme of the chat tab URIs, rune-agent://<model>/<id>.
	chatScheme = "rune-agent"
	// newViewFlag makes the chat command open another view of a chat a
	// tab shows already, rather than show that tab.
	newViewFlag = "--new"

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
	commandAttach        = "chatattach"
	commandClose         = "chatclose"
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

// loadMCPServers returns the MCP servers of the user configuration's
// mcpServers merged with the ones the workspace .mcp.json declares.
// Invalid configuration is reported through noti and contributes no
// servers.
func loadMCPServers(
	fs workspaceapi.FileSystem, root string, pconfig config.Config,
	noti browserapi.Notifications,
) map[string]runemcp.ServerConfig {
	var user map[string]runemcp.ServerConfig
	if raw, err := pconfig.GetMap("mcpServers"); err == nil {
		if user, err = runemcp.ParseServers(raw); err != nil {
			_, _ = noti.Notify(browserapi.LevelWarn, "mcpServers: invalid config: %v", err)
		}
	} else if !errors.Is(err, config.ErrNotFound) {
		_, _ = noti.Notify(browserapi.LevelWarn, "mcpServers: invalid config: %v", err)
	}

	var workspace map[string]runemcp.ServerConfig
	data, err := readWorkspaceFile(fs, filepath.Join(root, ".mcp.json"))
	switch {
	case err == nil:
		cfg, err := runemcp.LoadConfig(data)
		if err != nil {
			_, _ = noti.Notify(browserapi.LevelWarn, ".mcp.json: invalid config: %v", err)
			break
		}
		workspace = cfg.MCPServers
	case !errors.Is(err, os.ErrNotExist):
		_, _ = noti.Notify(browserapi.LevelWarn, "read .mcp.json: %v", err)
	}
	return runemcp.Merge(user, workspace)
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
	if webSearch := webSearchTool(pconfig, w.Notifications(ctx), os.Getenv); webSearch != nil {
		tools = append(tools, webSearch)
	}

	mcpManager := runemcp.NewManager(executor, cwd.Path(),
		runemcp.NewCredentialStore(w.DataDir(ctx), db))
	mcpServers := loadMCPServers(fs, cwd.Path(), pconfig, w.Notifications(ctx))

	ret = new(aiEditorHandler)
	ret.ctx, ret.cancelCtx = context.WithCancel(context.Background())
	ret.ed = ed
	ret.config = cfg
	ret.mcpManager = mcpManager
	ret.mcpServers = mcpServers
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
	ret.clip = text.NewSystemClipboard()
	ret.cfg = dialoguetui.ReadConfig(pconfig, hostConfig, ret.clip, noti)
	ret.cfg.MarkdownConfig.Parser = w.Parser(ctx)

	ret.queryDefaultModel = queryModelAlias

	ret.compactModel = compactModelAlias

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

	ret.mcpTrust = newMCPTrust(db, w.Prompter(ctx))
	ret.wireMCPManager()
	// Starting an MCP server needs StartCommand authorization or the
	// user's trust, which block until the user answers a prompt.
	// Connect in the background so the agent registers its commands
	// immediately.
	if len(mcpServers) > 0 {
		go debug.CapturePanicReport(func() {
			ret.connectMCPServers(mcpServers)
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

// statusBarImagesPartition caches the images status bar layouts download.
const statusBarImagesPartition = "status_bar_images"

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
	// statusBarMu guards cfg.StatusBar, cfg.InputBox and
	// cfg.InputBackgroundColor, which a config reload replaces.
	statusBarMu         sync.Mutex
	cfg                 dialoguetui.ComponentConfig
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
	mcpTrust            *mcpTrust
	mcpServers          map[string]runemcp.ServerConfig
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

	// openChats maps the dialogue ID of a running chat to its
	// *chatSession, or to struct{} while it starts.
	openChats sync.Map
	// lastCursor and lastChatID mirror the caret position and the chat
	// that last had focus. Workspace commands run once the prompt owns
	// the caret and from outside any chat tab, so both have to be read
	// back from state captured as the events happened.
	lastCursor atomic.Value // *cursorLocation
	lastChatID atomic.Value // *string
	// focusedViews maps a running chat's dialogue ID to the chatSender
	// of its view last focused, letting workspace command-prompt
	// commands inject in-chat commands into it.
	focusedViews sync.Map
	ctx          context.Context
	cancelCtx    func()

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

// replaceMCPTools applies a change to an MCP server's tools: removed
// leave the base set and added join it. The change reaches every
// subscribed conversation, so servers that connect, sign in or change
// their tools mid-session update chats that are already open.
func (h *aiEditorHandler) replaceMCPTools(server string, added []agent.Tool, removed []string) {
	if len(added) == 0 && len(removed) == 0 {
		return
	}
	h.baseToolsMu.Lock()
	defer h.baseToolsMu.Unlock()
	if len(removed) > 0 {
		h.baseTools = slices.DeleteFunc(h.baseTools, func(t agent.Tool) bool {
			return slices.Contains(removed, t.Definition().Function.Name)
		})
	}
	h.baseTools = append(h.baseTools, added...)
	h.toolRegistry.Remove(removed...)
	h.toolRegistry.Add(added...)
	for _, regs := range h.chatRegistries {
		for _, r := range regs {
			r.Remove(removed...)
			r.Add(added...)
		}
	}
}

// subscribeTools registers a conversation's registries, built from
// snapshot, to receive later MCP tool changes. Whatever changed between
// the snapshot and the subscription is applied immediately, under the
// same lock replaceMCPTools uses, so no change can fall between them.
func (h *aiEditorHandler) subscribeTools(
	id string, snapshot []agent.Tool, regs ...*agent.Registry,
) {
	h.baseToolsMu.Lock()
	defer h.baseToolsMu.Unlock()
	stale := make(map[string]llmapi.Tool, len(snapshot))
	for _, t := range snapshot {
		def := t.Definition()
		stale[def.Function.Name] = def
	}
	var added []agent.Tool
	for _, t := range h.baseTools {
		def := t.Definition()
		old, ok := stale[def.Function.Name]
		delete(stale, def.Function.Name)
		// A server that redefines a tool keeps its name, so compare the
		// definitions too.
		if !ok || !reflect.DeepEqual(old, def) {
			added = append(added, t)
		}
	}
	removed := slices.Collect(maps.Keys(stale))
	for _, r := range regs {
		r.Remove(removed...)
		r.Add(added...)
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

// wireMCPManager routes the MCP manager's events to the user: changed
// tools to every conversation, and servers that stop or need sign-in to
// a notification and to every open chat.
func (h *aiEditorHandler) wireMCPManager() {
	h.mcpManager.OnServerExit = func(name, detail string) {
		_, _ = h.n.Notify(browserapi.LevelError, "MCP server %q %s", name, detail)
		h.warnChatsAboutMCPServer(name)
	}
	h.mcpManager.OnToolsChanged = h.replaceMCPTools
	h.mcpManager.OnNeedsAuth = func(name string) {
		_, _ = h.n.Notify(browserapi.LevelWarn,
			"MCP server %q needs sign-in: run `agent mcp login %s`", name, name)
		h.warnChatsAboutMCPServer(name)
	}
	h.mcpManager.Open = h.openURL
}

// mcpChatWarning explains to a conversation why the tools of srv are
// missing, or returns "" when they are not.
func mcpChatWarning(srv *runemcp.ServerInfo) string {
	switch srv.Status {
	case runemcp.StatusConnecting:
		return fmt.Sprintf("MCP server %q is still starting. "+
			"Its tools will join this conversation once connected.", srv.Name)
	case runemcp.StatusNeedsAuth:
		reason := srv.Error
		if reason == "" {
			reason = fmt.Sprintf("needs sign-in: run `agent mcp login %s`", srv.Name)
		}
		return fmt.Sprintf("MCP server %q %s. "+
			"Its tools will join this conversation once you sign in.", srv.Name, reason)
	case runemcp.StatusError:
		if srv.Blocked {
			return fmt.Sprintf("MCP server %q is unavailable: %s", srv.Name, srv.Error)
		}
		return fmt.Sprintf("MCP server %q is unavailable: %s\n"+
			"Run `agent mcp reconnect %s` to try again.", srv.Name, srv.Error, srv.Name)
	}
	return ""
}

// warnUnavailableMCPServers tells a freshly opened conversation which
// MCP servers are still starting, wait for sign-in or failed, so missing
// tools are explained rather than silently absent.
func (h *aiEditorHandler) warnUnavailableMCPServers(tx chan<- dialoguetui.MessageEvent) {
	if h.mcpManager == nil {
		return
	}
	var lines []string
	for _, srv := range h.mcpManager.Servers() {
		if w := mcpChatWarning(srv); w != "" {
			lines = append(lines, w)
		}
	}
	if len(lines) == 0 {
		return
	}
	tx <- dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventWarning,
		Text: strings.Join(lines, "\n"),
	}
}

// warnChatsAboutMCPServer tells every open chat why the server named
// name became unavailable, so a chat that was promised its tools, or
// relied on them, learns why they are missing. Chats opened later learn
// it from warnUnavailableMCPServers.
func (h *aiEditorHandler) warnChatsAboutMCPServer(name string) {
	var text string
	for _, srv := range h.mcpManager.Servers() {
		// A server back to starting is being reconnected, which settles
		// in its own warning or in its tools joining.
		if srv.Name == name && srv.Status != runemcp.StatusConnecting {
			text = mcpChatWarning(srv)
		}
	}
	if text == "" {
		return
	}
	// Logged rather than sent through the agent loop's event channel,
	// which closes with the chat; a chat that ended ignores it.
	h.openChats.Range(func(_, v any) bool {
		if s, ok := v.(*chatSession); ok {
			s.publish(messageEvent(dialoguetui.MessageEvent{
				Type: dialoguetui.MessageEventWarning, Text: text,
			}))
		}
		return true
	})
}

// connectMCPServers brings up every configured MCP server concurrently,
// keeping the user informed: a progress notification stays open while a
// server starts (authorization may hold it for a while), and resolves
// into a success, warning or error notification. Tools reach the base
// set and every open conversation as each server comes up.
func (h *aiEditorHandler) connectMCPServers(servers map[string]runemcp.ServerConfig) {
	var wg sync.WaitGroup
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			h.connectMCPServer(name, servers[name])
		})
	}
	wg.Wait()
}

func (h *aiEditorHandler) connectMCPServer(name string, raw runemcp.ServerConfig) {
	cfg, err := h.prepareMCPServer(h.ctx, name, raw)
	if err != nil {
		if h.ctx.Err() == nil {
			_, _ = h.n.Notify(browserapi.LevelWarn, "MCP server %q is not connected: %v", name, err)
			h.warnChatsAboutMCPServer(name)
		}
		return
	}
	id, nerr := h.n.Notify(browserapi.LevelInfo, "Starting MCP server %q…", name)
	if nerr == nil {
		_ = h.n.UpdateNotificationProgress(id, "", 0, 1)
	}
	tools, err := h.mcpManager.ConnectServer(h.ctx, name, cfg)
	if nerr == nil {
		_ = h.n.UpdateNotificationProgress(id, "", 1, 1)
	}
	switch {
	case errors.Is(err, runemcp.ErrNeedsAuth):
		_, _ = h.n.Notify(browserapi.LevelWarn,
			"MCP server %q needs sign-in: run `agent mcp login %s`", name, name)
		h.warnChatsAboutMCPServer(name)
	case err != nil:
		_, _ = h.n.Notify(browserapi.LevelError,
			"MCP server %q failed to start: %v", name, err)
		h.warnChatsAboutMCPServer(name)
	default:
		h.replaceMCPTools(name, tools, nil)
		_, _ = h.n.Notify(browserapi.LevelSuccess,
			"MCP server %q is up: %d tools available", name, len(tools))
	}
}

// prepareMCPServer expands the environment variables in the server's
// config raw and asks for the user's trust where needed, recording the
// server with the manager either way. It returns the config to connect
// with, or why the server must not connect, which the manager keeps as
// the server's error until the server is prepared again.
func (h *aiEditorHandler) prepareMCPServer(
	ctx context.Context, name string, raw runemcp.ServerConfig,
) (runemcp.ServerConfig, error) {
	cfg, exp, err := runemcp.ExpandConfig(raw, os.LookupEnv)
	if err != nil {
		h.mcpManager.Register(name, raw, nil, err)
		return runemcp.ServerConfig{}, err
	}
	var warnings []string
	for _, v := range exp.Missing {
		warnings = append(warnings, fmt.Sprintf("environment variable %s is not set", v))
	}
	if kind, _ := cfg.Transport(); kind != runemcp.TransportStdio && h.cwd.Scheme() != "file" {
		if u, err := url.Parse(cfg.URL); err == nil && runemcp.IsLoopbackHost(u.Hostname()) {
			warnings = append(warnings, fmt.Sprintf(
				"%s resolves on this machine, not the workspace host", u.Host))
			_, _ = h.n.Notify(browserapi.LevelWarn,
				"MCP server %q uses %s, which resolves on this machine, not the workspace host",
				name, u.Host)
		}
	}
	h.mcpManager.Register(name, cfg, warnings, nil)

	ok, err := h.mcpTrust.Allow(ctx, h.cwd, name, raw, exp)
	if err == nil && !ok {
		err = fmt.Errorf("not trusted: run `agent mcp trust %s`", name)
	}
	if err != nil {
		h.mcpManager.Register(name, cfg, warnings, err)
		return runemcp.ServerConfig{}, err
	}
	return cfg, nil
}

// openURL shows an http or https URL through the host, which opens it
// in the user's browser.
func (h *aiEditorHandler) openURL(rawURL string) error {
	uri, err := workspaceapi.ParseURI(rawURL)
	if err != nil {
		return err
	}
	if s := uri.Scheme(); s != "http" && s != "https" {
		return fmt.Errorf("refusing to open %s URL", s)
	}
	return h.o.Show(uri)
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
	h.openChats.Range(func(_, v any) bool {
		if s, ok := v.(*chatSession); ok {
			s.agent.SetMaxOutputTokens(n)
			s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
				st.MaxTokens = n
			})
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

// reloadStyle configures the status bar, the compose box frame and the
// compose box colours of the open chats, and of those opened from now on,
// with the status_bar block, input_box_frame_charset and input box colours
// host holds for this extension, reading their images again. host is the
// config Rune serves; a host that could not be fetched leaves the chats as
// they are.
func (h *aiEditorHandler) reloadStyle(ctx context.Context, host config.Config) error {
	own, err := ownConfig(host)
	if err != nil {
		return err
	}
	h.statusBarMu.Lock()
	cfg := h.cfg
	h.statusBarMu.Unlock()
	cfg, frameImages := dialoguetui.ReadStyle(ctx, cfg, own, host, h.n)

	h.statusBarMu.Lock()
	h.cfg.StatusBar = cfg.StatusBar
	h.cfg.InputBox.FrameCharSet = cfg.InputBox.FrameCharSet
	h.cfg.InputBox.FrameAttr = cfg.InputBox.FrameAttr
	h.cfg.InputBox.ContentAttr = cfg.InputBox.ContentAttr
	h.cfg.InputBackgroundColor = cfg.InputBackgroundColor
	h.statusBarMu.Unlock()
	h.openChats.Range(func(_, v any) bool {
		// Chats still opening hold no component yet; they read the
		// config they open with afterwards.
		if s, ok := v.(*chatSession); ok {
			s.restyle(cfg, frameImages)
		}
		return true
	})
	return h.p.Interrupt(ctx)
}

// ownConfig returns the config Rune started this extension with, which
// host, the config Rune serves, holds under extensions.<id>.config. A host
// without it gives an empty config. It fails when host could not be
// fetched.
func ownConfig(host config.Config) (config.Config, error) {
	cfg := host
	for _, key := range []string{"extensions", extensionID, "config"} {
		sub, err := cfg.GetConfig(key)
		if errors.Is(err, config.ErrNotFound) {
			return config.NopConfig(), nil
		}
		if err != nil {
			return nil, fmt.Errorf("get %q from the config: %w", key, err)
		}
		cfg = sub
	}
	return cfg, nil
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
	case commandAttach:
		return h.handleChatAttach(cmd)
	case commandClose:
		return h.handleChatClose(cmd)
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
// that model is still available and the default model otherwise. A chat
// that is running gets a new view, shown at uri.
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
// order the store lists them. A running chat is offered as the URI of its
// tab last focused, so opening it shows that tab rather than another view.
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
	if s, ok := h.runningChat(d.ID); ok {
		return s.shownURI().String()
	}
	uri, err := getModelUri(d.ID, cmp.Or(d.Model, h.defaultModel))
	if err != nil {
		// newChat cannot open such a dialogue either.
		return ""
	}
	return uri.String()
}

// runningChat returns the session of the chat id when it runs.
func (h *aiEditorHandler) runningChat(id string) (*chatSession, bool) {
	v, ok := h.openChats.Load(id)
	if !ok {
		return nil, false
	}
	s, ok := v.(*chatSession)
	return s, ok
}

// runningChats returns the IDs of the running chats, those no tab shows
// first, each lot sorted.
func (h *aiEditorHandler) runningChats() []string {
	var detached, shown []string
	h.openChats.Range(func(_, v any) bool {
		if s, ok := v.(*chatSession); ok {
			if s.detached() {
				detached = append(detached, s.id)
			} else {
				shown = append(shown, s.id)
			}
		}
		return true
	})
	slices.Sort(detached)
	slices.Sort(shown)
	return append(detached, shown...)
}

// handleChatAttach shows another view of a running chat, such as one no
// tab shows since its tab closed while it worked.
func (h *aiEditorHandler) handleChatAttach(cmd textapi.Command) error {
	if len(cmd.Args) != 1 {
		return fmt.Errorf("usage: %s <dialogue_id>", cmd.Name)
	}
	id := parseDialogueID(cmd)
	s, ok := h.runningChat(id)
	if !ok {
		return fmt.Errorf("agent chat %q is not running; open it with %s %s", id, commandChat, id)
	}
	c, err := s.attach(workspaceapi.URI{})
	if err != nil {
		return err
	}
	return h.showChat(cmd.Window, c)
}

// handleChatClose ends a running chat in every view: the chat cmd names,
// or the one of the focused tab.
func (h *aiEditorHandler) handleChatClose(cmd textapi.Command) error {
	var id string
	switch len(cmd.Args) {
	case 0:
		var ok bool
		if id, ok = dialogueIDFromURI(cmd.URI); !ok {
			return fmt.Errorf("%s takes a dialogue id outside of an agent chat tab", cmd.Name)
		}
	case 1:
		id = parseDialogueID(cmd)
	default:
		return fmt.Errorf("usage: %s [<dialogue_id>]", cmd.Name)
	}
	s, ok := h.runningChat(id)
	if !ok {
		return fmt.Errorf("agent chat %q is not running", id)
	}
	s.end(sessionEndChatClose)
	return nil
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
	v, ok := h.focusedViews.Load(id)
	if !ok {
		return fmt.Errorf("%s must be run from an open agent chat tab", cmd.Name)
	}
	view := v.(chatSender)

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
	// The view takes ev when its handler gets to it, which must not
	// hold the event loop.
	go debug.CapturePanicReport(func() { view.send(ev) })
	return nil
}

func (h *aiEditorHandler) Complete(ctx context.Context, name string, args []string) (
	iterator.Iterator[string], error,
) {
	slog.Debug("complete with", "name", name, "args", args)

	// Strip the flags from args for length checks, pass --all through
	// to the completer.
	filtered, showAll := withoutFlag(args, "--all")
	filtered, _ = withoutFlag(filtered, newViewFlag)

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
	case commandAttach, commandClose:
		if len(filtered) > 1 {
			return iterator.FromSlice[string](nil), nil
		}
		return iterator.FromSlice(h.runningChats()), nil
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
		agentshell.WithMCPInfo(mcpCommands{Manager: h.mcpManager, h: h}),
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
	var another bool
	cmd.Args, another = withoutFlag(cmd.Args, newViewFlag)
	if s, ok := h.runningChat(parseDialogueID(cmd)); ok && !another && !s.detached() {
		return h.focusChat(cmd.Window, s)
	}
	c, err := h.newChat(cmd, workspaceapi.URI{})
	if err != nil {
		return err
	}
	return h.showChat(cmd.Window, c)
}

// focusChat shows in win the tab of s last focused.
func (h *aiEditorHandler) focusChat(win browserapi.Window, s *chatSession) error {
	tab, err := h.o.Open(s.shownURI())
	if err == nil {
		err = h.wm.SetWindowContent(win, tab)
	}
	if err != nil {
		return fmt.Errorf("show agent chat %q: %w; %s %s %s opens another view",
			s.id, err, commandChat, newViewFlag, s.id)
	}
	return nil
}

// showChat shows c in a tab of its own in win.
func (h *aiEditorHandler) showChat(win browserapi.Window, c *chat) error {
	tab, err := openChatTab(h.wm, c.uri, c.id, c.content)
	if err != nil {
		_ = c.content.Close()
		return err
	}
	if err := h.wm.SetWindowContent(win, tab); err != nil {
		return fmt.Errorf("window set content: %v", err)
	}
	return nil
}

// chat is a view of an agent chat: its content, which closes the view
// when closed, and the identity it goes by as a tab.
type chat struct {
	content browserapi.Handler
	id      string
	uri     workspaceapi.URI
}

// newChat opens a view of the chat cmd names, or of a new one, shown at
// uri, or at a URI derived from the dialogue and its model when uri is
// zero. A chat that is not running starts with the model cmd names, or
// the default one; a running one keeps its own.
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

	d, err := h.getDialogue(h.ctx, h.dialogueStore, cmd)
	if err != nil {
		return nil, err
	}
	for {
		v, loaded := h.openChats.LoadOrStore(d.ID, struct{}{})
		if !loaded {
			break
		}
		s, ok := v.(*chatSession)
		if !ok {
			return nil, fmt.Errorf("agent chat %q is still opening", d.ID)
		}
		c, err := s.attach(uri)
		if !errors.Is(err, errChatClosed) {
			return c, err
		}
		select {
		case <-s.done:
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		}
	}
	s, err := h.startChat(d, model, uri)
	if err != nil {
		h.openChats.Delete(d.ID)
		return nil, err
	}
	h.openChats.Store(d.ID, s)
	c, err := s.attach(uri)
	if err != nil {
		s.end(sessionEndTabClose)
		return nil, err
	}
	return c, nil
}

// startChat starts the session of the chat of d with model. Its views
// are shown at uri, or at the URI derived from the dialogue and its
// model when uri is zero.
func (h *aiEditorHandler) startChat(
	d dialoguemanager.Dialogue, model string, uri workspaceapi.URI,
) (*chatSession, error) {
	backendService, chatEntry, err := h.modelService(h.ctx, model)
	if err != nil {
		return nil, fmt.Errorf("new backend: %v", err)
	}
	if uri == (workspaceapi.URI{}) {
		uri, err = getModelUri(d.ID, model)
	} else {
		uri, err = chatBaseURI(uri)
	}
	if err != nil {
		return nil, err
	}
	s := newChatSession(h, d.ID, uri)
	syncComp := syncComponent{h: h, tab: s.tab, session: s}

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
		agentshell.WithMCPInfo(mcpCommands{Manager: h.mcpManager, h: h}),
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
		session:       s,
	})
	s.commands = adapter
	s.publish(chatEvent{Kind: chatEventHistory, History: d.Messages})

	prompter := &tuiPrompter{chat: syncComp}
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
	progressUpdater := newTUIProgressUpdater(s.tx, s.tab)
	taskTools := agentools.NewTaskTools(taskStore, progressUpdater)
	allTools := make([]agent.Tool, 0, len(baseTools)+len(sessionTools)+len(taskTools)+5)
	allTools = append(allTools, baseTools...)
	allTools = append(allTools, sessionTools...)
	allTools = append(allTools, askUser)
	allTools = append(allTools, requestSkill)
	allTools = append(allTools, exitPlan)
	allTools = append(allTools, skillTool) // overrides nil-spawner skill tool from baseTools
	allTools = append(allTools, taskTools...)
	s.webBrowser = h.webBrowser(d.ID)
	if s.webBrowser != nil {
		allTools = append(allTools, s.webBrowser)
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
	s.agent = chatAgent
	syncComp.setStatusBarState(func(s *dialoguetui.StatusBarState) {
		s.Conversation = d.ID
	})
	adapter.syncStatusBarModel()
	// Token counting walks the whole replayed history, so keep it off
	// the path that opens the tab.
	go debug.CapturePanicReport(func() {
		syncComp.seedContextTokens(h.llmSvc, chatAgent.ModelEntry(), d.Messages)
		_ = h.p.Interrupt(s.ctx)
	})
	h.subscribeTools(d.ID, baseTools, chatRegistry, cmdRegistry)
	s.run()
	h.warnUnavailableMCPServers(s.tx)

	go debug.CapturePanicReport(func() {
		createAgentCompletions(s.ctx, s.cancel, s.tx, s.requests, chatAgent, spawner, childEvents,
			h.skillRegistry, d.ID, syncComp,
			makeOnCompacted(h.dialogueStore, adapter.compactFn),
			h.dialogueStore)
	})
	return s, nil
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
				select {
				case ret <- h.newCompletionRequest(reqCtx, msg):
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

// newCompletionRequest returns the request that answers msg within ctx.
func (h *aiEditorHandler) newCompletionRequest(
	ctx context.Context, msg dialoguetui.SubmitMessage,
) completionRequest {
	finalized := finalizeSubmitMessage(msg)
	return completionRequest{
		displayText: finalized.DisplayText,
		modelText:   finalized.ModelText,
		skillName:   msg.SkillName,
		attachments: h.attachmentContentParts(ctx, finalized.Attachments),
		ctx:         ctx,
	}
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

// withoutFlag returns args with flag removed, and whether it was there.
func withoutFlag(args []string, flag string) ([]string, bool) {
	out := slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == flag })
	return out, len(out) != len(args)
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

// childToolMessage returns the chat event that shows ev, a tool call or
// tool result of a sub-agent, nested under the tool call parentID.
func childToolMessage(parentID string, ev agent.Event) dialoguetui.MessageEvent {
	msg := dialoguetui.MessageEvent{
		Type:             dialoguetui.MessageEventToolCall,
		ToolCallID:       ev.ToolCallID,
		ToolName:         ev.ToolName,
		ToolArgs:         ev.ToolArgs,
		ToolSummary:      ev.ToolSummary,
		ToolStartTime:    ev.ToolStartTime,
		ParentToolCallID: parentID,
	}
	if ev.Type == agent.EventToolResult {
		msg.Type = dialoguetui.MessageEventToolResult
		msg.ToolOutput = ev.ToolOutput
		msg.IsError = ev.IsError
		msg.ToolDuration = ev.ToolDuration
		msg.ImagePaths = toolResultImages(ev.ToolName, ev.ToolOutput, ev.IsError)
	}
	return msg
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
						msg = childToolMessage(cev.ParentToolCallID, cev.Event)
					case agent.EventToolResult:
						msg = childToolMessage(cev.ParentToolCallID, cev.Event)
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
		// Signal that the agent is idle; the handler will drain any
		// queued follow-up messages at this point.
		idle := func() bool {
			select {
			case tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy, Busy: false}:
				return true
			case <-ctx.Done():
				return false
			}
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
			if !idle() {
				return
			}
			continue
		}

		// Agent-type skills run in a sub-agent, shown like one the
		// model spawns with the skill tool.
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
					Skill:             skill.Name,
					Model:             llmarg.Qualify(ag.ModelEntry()),
					Message:           req.modelText,
					DisplayMessage:    req.displayText,
					Attachments:       req.attachments,
					AdditionalContext: upsRes.AdditionalContext,
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
					if !idle() {
						return
					}
					continue
				}
				it = newAgentSkillEvents(skill, handle, tx, syncComp.tab,
					func(ctx context.Context, replies []llmapi.Message) error {
						return ag.AppendTurn(ctx, id, req.modelText, replies,
							agent.WithDisplayMessage(req.displayText),
							agent.WithAttachments(req.attachments))
					})
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
		if !idle() {
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
