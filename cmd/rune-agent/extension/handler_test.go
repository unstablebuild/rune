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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	stdimage "image"
	"image/color"
	"image/png"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
)

func TestAddMessageHiddenToolImages(t *testing.T) {
	image := llmapi.ContentPart{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,aW1hZ2U="}
	for _, tt := range []struct {
		name   string
		msg    llmapi.Message
		hidden bool
	}{
		{"tool image", llmapi.Message{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{image}}, true},
		{"tool hint and image", llmapi.Message{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: "Generated image saved to /tmp/image.png"}, image,
		}}, true},
		{"user text and image", llmapi.Message{Role: llmapi.RoleUser, Content: "edit this", MultiContent: []llmapi.ContentPart{image}}, false},
		{"image attachment without text", llmapi.Message{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText},
			{Type: llmapi.ContentPartTypeText, Text: "Attached image /tmp/reference.png:"}, image,
		}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			comp := dialoguetui.NewComponent(defaultComponentCfg)
			t.Cleanup(func() { require.NoError(t, comp.Close()) })
			addMessage(comp, llmapi.Message{Role: llmapi.RoleUser, Content: "generate an image"}, nil)
			before := comp.Height(80)
			addMessage(comp, tt.msg, nil)
			if tt.hidden {
				assert.Equal(t, before, comp.Height(80), "tool images must not add empty sent bubbles")
			} else {
				assert.Greater(t, comp.Height(80), before, "real user messages and attachments must remain visible")
			}
		})
	}
}

func TestAddMessageGeneratedImageReplay(t *testing.T) {
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, stdimage.NewRGBA(stdimage.Rect(0, 0, 2, 2))))
	dir := t.TempDir()
	svc := &imageGenerationService{image: llmapi.Image{PNG: data.Bytes()}}
	tool := agentools.NewImageGen(svc, llmapi.ModelEntry{Provider: "codex"}, testLocalFS{root: dir}, dir)
	ctx := agent.WithDialogueID(t.Context(), "chat")
	ctx = agent.WithParentToolCallID(ctx, "call")
	const args = `{"prompt":"a blue bird"}`
	result := tool.Execute(ctx, args)
	require.False(t, result.IsError, result.Content)
	imagePath, ok := agentools.GeneratedImagePath(result.Content)
	require.True(t, ok)

	for _, collapsed := range []bool{true, false} {
		t.Run(fmt.Sprintf("collapsed=%v", collapsed), func(t *testing.T) {
			cfg := defaultComponentCfg
			cfg.StartCollapsed = collapsed
			live := dialoguetui.NewComponent(cfg)
			replayed := dialoguetui.NewComponent(cfg)
			t.Cleanup(func() {
				require.NoError(t, live.Close())
				require.NoError(t, replayed.Close())
			})
			live.AddSendMessage("draw a bird")
			live.AddToolCall("call", "imagegen", args, tool.Summary(args))
			live.CompleteToolCall("call", "imagegen", args, tool.Summary(args), result.Content, false)
			live.AddToolCallImage("call", imagePath, tool.Summary(args))
			live.AddReceiveMessage("Done.")
			pending := make(map[string]llmapi.ToolCall)
			for _, msg := range []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "draw a bird"},
				{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{{ID: "call", Function: llmapi.FunctionCall{Name: "imagegen", Arguments: args}}}},
				{Role: llmapi.RoleTool, ToolCallID: "call", Content: result.Content},
				{Role: llmapi.RoleUser, MultiContent: result.MultiContent},
				{Role: llmapi.RoleAssistant, Content: "Done."},
			} {
				addMessage(replayed, msg, pending)
			}
			require.Empty(t, pending)
			assert.Equal(t, live.Height(80), replayed.Height(80))
			draw := func(comp *dialoguetui.Component) string {
				comp.Resize(80, 30)
				writer := term.NewStringWriter(80, 30)
				comp.Draw(writer)
				require.NoError(t, writer.Flush())
				return writer.String()
			}
			actual := draw(replayed)
			assert.Equal(t, draw(live), actual)
			wantCaptions := 2
			if !collapsed {
				wantCaptions++ // Expanded tools also show their arguments.
			}
			assert.Equal(t, wantCaptions, strings.Count(actual, "a blue bird"), "tool summary and image caption must both survive replay")
		})
	}
	assert.Equal(t, []string{imagePath}, toolResultImages("imagegen", result.Content, false))
	assert.Empty(t, toolResultImages("imagegen", result.Content, true))
	assert.Empty(t, toolResultImages("read_file", result.Content, false))
}

func TestCreateAgentCompletionsChildImages(t *testing.T) {
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, stdimage.NewRGBA(stdimage.Rect(0, 0, 2, 2))))
	dir := t.TempDir()
	svc := &imageGenerationService{image: llmapi.Image{PNG: data.Bytes()}}
	tool := agentools.NewImageGen(svc, llmapi.ModelEntry{Provider: "codex"}, testLocalFS{root: dir}, dir)
	toolCtx := agent.WithParentToolCallID(agent.WithDialogueID(t.Context(), "child"), "call")
	const args = `{"prompt":"a blue bird"}`
	result := tool.Execute(toolCtx, args)
	require.False(t, result.IsError, result.Content)
	imagePath, ok := agentools.GeneratedImagePath(result.Content)
	require.True(t, ok)

	ctx, cancel := context.WithCancel(t.Context())
	tx := make(chan dialoguetui.MessageEvent)
	childEvents := make(chan agent.ChildEvent)
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		createAgentCompletions(ctx, cancel, tx, nil, nil, nil,
			childEvents, nil, "parent", newStatusBarSyncComponent(), nil, newMemDialogueStore())
	})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	for _, tt := range []struct {
		name      string
		toolName  string
		isError   bool
		wantPaths []string
	}{
		{name: "generated image", toolName: "imagegen", wantPaths: []string{imagePath}},
		{name: "failed generation", toolName: "imagegen", isError: true},
		{name: "ordinary tool", toolName: "read_file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := agent.ChildEvent{ParentToolCallID: "spawn", Event: agent.Event{
				Type: agent.EventToolResult, ToolCallID: "call", ToolName: tt.toolName,
				ToolArgs: args, ToolSummary: "a blue bird", ToolOutput: result.Content, IsError: tt.isError,
			}}
			select {
			case childEvents <- event:
			case <-time.After(3 * time.Second):
				t.Fatal("child event was not consumed")
			}
			select {
			case msg := <-tx:
				assert.Equal(t, dialoguetui.MessageEventToolResult, msg.Type)
				assert.Equal(t, "spawn", msg.ParentToolCallID)
				assert.Equal(t, "call", msg.ToolCallID)
				assert.Equal(t, "a blue bird", msg.ToolSummary)
				assert.Equal(t, tt.wantPaths, msg.ImagePaths)
			case <-time.After(3 * time.Second):
				t.Fatal("child result was not forwarded")
			}
		})
	}
}

type imageGenerationService struct {
	llmapi.Service
	available bool
	err       error
	checks    int
	image     llmapi.Image
	// images, when set, are returned one per request before falling back to image.
	images []llmapi.Image

	mu       sync.Mutex
	requests []llmapi.ImageRequest
}

func (s *imageGenerationService) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	s.checks++
	return s.available, s.err
}

func (s *imageGenerationService) GenerateImage(_ context.Context, _ llmapi.ModelEntry, request llmapi.ImageRequest) (llmapi.Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	if len(s.images) > 0 {
		generated := s.images[0]
		s.images = s.images[1:]
		return generated, s.err
	}
	return s.image, s.err
}

func (s *imageGenerationService) imageRequests() []llmapi.ImageRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func TestE2EImageGenEditsEarlierImagesFromTheSameRun(t *testing.T) {
	solidPNG := func(c color.Color) []byte {
		img := stdimage.NewRGBA(stdimage.Rect(0, 0, 2, 2))
		for y := range 2 {
			for x := range 2 {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		return buf.Bytes()
	}
	red := solidPNG(color.RGBA{R: 255, A: 255})
	blue := solidPNG(color.RGBA{B: 255, A: 255})
	combined := solidPNG(color.RGBA{R: 255, B: 255, A: 255})
	imagegen := func(id, args string) llmtest.Response {
		return llmtest.Response{ToolCalls: []llmapi.ToolCall{{
			ID: id, Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionCall{Name: "imagegen", Arguments: args},
		}}, FinishReason: llmapi.FinishReasonToolCall}
	}
	model := llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000}
	scripted := llmtest.New([]llmapi.ModelEntry{model},
		imagegen("draw-red", `{"prompt":"a red square"}`),
		imagegen("draw-blue", `{"prompt":"a blue square"}`),
		imagegen("combine", `{"prompt":"put both squares side by side","num_last_images_to_include":2}`),
		llmtest.Response{Chunks: []string{"done"}, FinishReason: llmapi.FinishReasonStop},
	)
	svc := &imageGenerationService{
		Service: scripted, available: true,
		images: []llmapi.Image{{PNG: red}, {PNG: blue}, {PNG: combined}},
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	dir := t.TempDir()
	fs, cwd := testLocalFS{root: dir}, dirURI(dir)
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   model.Name,
		dialogueStore:  newMemDialogueStore(),
		wm:             &recordingWindowManager{},
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, cwd, nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            cwd,
		fs:             fs,
		memoryDataPath: t.TempDir(),
		imagesDir:      filepath.Join(dir, "generated_images"),
	}
	c, err := h.newChat(textapi.Command{Args: []string{"painting-fox"}}, workspaceapi.URI{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.content.Close()) })

	const width, height = 100, 60
	c.content.Resize(width, height)
	keys, err := term.ParseKeys("draw<enter>")
	require.NoError(t, err)
	for _, k := range keys {
		c.content.Handle(term.Event{Type: term.EventKey, Mod: k.Mod, Key: k.Key, Ch: k.Ch})
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, 4, scripted.CallCount())
	}, 5*time.Second, 10*time.Millisecond)

	requests := svc.imageRequests()
	require.Len(t, requests, 3)
	assert.Empty(t, requests[0].Images)
	assert.Empty(t, requests[1].Images)
	assert.Equal(t, [][]byte{red, blue}, requests[2].Images,
		"the edit must receive both images generated earlier in the run, oldest first")

	var sent []string
	for _, m := range scripted.Requests()[3].Request.Messages {
		if m.Role != llmapi.RoleUser {
			continue
		}
		for _, part := range m.MultiContent {
			if part.Type == llmapi.ContentPartTypeImageURL {
				sent = append(sent, part.ImageURL)
			}
		}
	}
	dataURI := func(data []byte) string {
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	}
	assert.Equal(t, []string{dataURI(red), dataURI(blue), dataURI(combined)}, sent,
		"the model must see every image generated in the run")
}

func TestHandlerImageGen(t *testing.T) {
	for _, tt := range []struct {
		name, provider, dir string
		available           bool
		err                 error
		want                bool
	}{
		{name: "paid codex", provider: "codex", dir: "/host/generated_images", available: true, want: true},
		{name: "no eligible login", provider: "codex", dir: "/host/generated_images"},
		{name: "openai API key", provider: "openai", dir: "/host/generated_images", available: true, want: true},
		{name: "future provider", provider: "future", dir: "/host/generated_images", available: true, want: true},
		{name: "unsupported anthropic", provider: "anthropic", dir: "/host/generated_images"},
		{name: "unsupported openai model", provider: "openai", dir: "/host/generated_images"},
		{name: "no host directory", provider: "codex", available: true},
		{name: "host error", provider: "codex", dir: "/host/generated_images", err: errors.New("offline")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &imageGenerationService{available: tt.available, err: tt.err}
			h := &aiEditorHandler{ctx: t.Context(), fs: nopFileSystem{}, imagesDir: tt.dir}
			model := llmapi.ModelEntry{Provider: tt.provider, Name: "model"}
			tool := h.imageGen(svc, model)
			if tt.want {
				require.NotNil(t, tool)
				assert.Equal(t, "imagegen", tool.Definition().Function.Name)
				svc.available = false
				assert.Nil(t, h.imageGen(svc, model), "recheck eligibility for the next chat")
				assert.Equal(t, 2, svc.checks)
			} else {
				assert.Nil(t, tool)
			}
			if tt.dir == "" {
				assert.Zero(t, svc.checks)
			}
			assert.Nil(t, h.imageGen(llmtest.New(nil), model), "unsupported service")
		})
	}
}

func TestHandlerRegisterImageGen(t *testing.T) {
	h := &aiEditorHandler{ctx: t.Context(), fs: nopFileSystem{}, imagesDir: "/host/generated_images"}
	registry := agent.NewRegistry()
	svc := &imageGenerationService{available: true}
	for _, provider := range []string{"openai", "codex", "future"} {
		model := llmapi.ModelEntry{Provider: provider, Name: "supported"}
		h.registerImageGen(registry, svc, model)
		_, ok := registry.Get("imagegen", provider)
		require.True(t, ok)
		_, ok = registry.Get("imagegen", "anthropic")
		require.False(t, ok)
		_, ok = registry.Get("imagegen", "")
		require.False(t, ok)

		svc.available = false
		model.Name = "unsupported"
		h.registerImageGen(registry, svc, model)
		_, ok = registry.Get("imagegen", provider)
		require.False(t, ok, "same-provider model switches must recheck eligibility")

		svc.available = true
		h.registerImageGen(registry, svc, model)
		_, ok = registry.Get("imagegen", provider)
		require.True(t, ok)
	}
}

func TestImageGenDir(t *testing.T) {
	for _, root := range []string{"/Users/u/.rune", "/home/dev/.runedev"} {
		got, err := imageGenDir(t.Context(), fakeInstallRoot{root: root})
		require.NoError(t, err)
		assert.Equal(t, root+"/generated_images", got)
	}
	_, err := imageGenDir(t.Context(), fakeInstallRoot{err: os.ErrNotExist})
	assert.ErrorContains(t, err, "data directory on the workspace host")
}

func TestDialogueConfigImages(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cache storageapi.Service
	}{
		{name: "image cache", cache: storagestub.NewInMemoryService()},
		{name: "no image cache"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := &aiEditorHandler{
				cfg: defaultComponentCfg, imageCache: tt.cache, p: term.NopInterrupter(),
				fs: nopFileSystem{}, cwd: dirURI("/workspace"),
			}
			cfg := h.dialogueConfig(new(sync.Mutex))
			require.NotSame(t, h.cfg.MarkdownConfig, cfg.MarkdownConfig)
			assert.Equal(t, h.cwd.Path(), cfg.MarkdownConfig.Dir)
			assert.Equal(t, h.fs, cfg.MarkdownConfig.FS)
			assert.Equal(t, tt.cache, cfg.MarkdownConfig.Storage)
			assert.Equal(t, tt.cache != nil, cfg.MarkdownConfig.Interrupter != nil)
			_, err := markdown.NewWithConfig("**hello**", *cfg.MarkdownConfig)
			assert.NoError(t, err, "transcript markdown must render")
			assert.Nil(t, h.cfg.MarkdownConfig.Storage, "shared defaults must remain unchanged")
		})
	}
}

func TestQueryFloatingCloseLeavesItsWindow(t *testing.T) {
	model := llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000}
	svc := llmtest.New([]llmapi.ModelEntry{model},
		llmtest.Response{Chunks: []string{"done"}, FinishReason: llmapi.FinishReasonStop})
	skillReg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
	store := newMemDialogueStore()
	wm := &previewWindowManager{open: make(map[uint64]browserapi.Floating)}
	h := &aiEditorHandler{
		ctx:           t.Context(),
		p:             term.NopInterrupter(),
		wm:            wm,
		n:             stubNotifications{},
		llmSvc:        svc,
		dialogueStore: store,
		skillRegistry: skillReg,
		agentsConfig:  &agent.Cfg{},
		queryAgent: agent.NewAgent(svc, agent.NewRegistry(), skillReg, store,
			agent.NoMemory(), agent.Config{SystemPrompt: "test", Model: model}),
	}
	require.NoError(t, h.handleQuery(textapi.Command{Args: []string{"hello"}}))
	require.Equal(t, []uint64{1}, wm.openIDs())

	require.NoError(t, wm.open[1].Close())

	assert.Equal(t, []uint64{1}, wm.openIDs(),
		"the window may already show other content when the query is released")
}

func TestProviderContentSearch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "literal.txt"), []byte("other\nneedle configuration value\n"), 0o644))
	fs := testLocalFS{root: dir}
	cwd := dirURI(dir)
	tools, tracker := agentools.DefaultTools(fs, testLocalExec{}, cwd, nil, agentools.NoIndex(), agentools.Config{}, configedit.NopConfig())
	registry := agent.NewRegistry(tools...)
	registerOpenAITools(registry, fs, cwd, agentools.NoIndex(), tracker, nil, configedit.NopConfig())
	for _, provider := range []string{"openai", "codex", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			tool, ok := registry.Get("search_content", provider)
			require.True(t, ok, "literal search must retain matching lines")
			result := tool.Execute(t.Context(), `{"pattern":"needle","path":null,"include":null}`)
			require.False(t, result.IsError, result.Content)
			assert.Contains(t, result.Content, "literal.txt:2:needle configuration value")
			if provider != "anthropic" {
				_, ok = registry.Get("grep_files", provider)
				assert.True(t, ok)
				_, ok = registry.Get("exec_command", provider)
				assert.True(t, ok)
				_, ok = registry.Get("bash", provider)
				assert.False(t, ok)
				assert.Empty(t, registry.ReplacementFor("search_content", provider))
			}
		})
	}
}

func TestRegisterOpenAIToolsKeepsCompact(t *testing.T) {
	dir := t.TempDir()
	fs, cwd := testLocalFS{root: dir}, dirURI(dir)
	tools, tracker := agentools.DefaultTools(fs, testLocalExec{}, cwd, nil, agentools.NoIndex(), agentools.Config{}, configedit.NopConfig())
	registry := agent.NewRegistry(tools...)
	registerOpenAITools(registry, fs, cwd, agentools.NoIndex(), tracker, nil, configedit.NopConfig())
	for _, provider := range []string{"openai", "codex", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			_, ok := registry.Get("compact", provider)
			assert.True(t, ok, "compact must be callable")
			var advertised []string
			for _, tool := range registry.Tools(provider) {
				advertised = append(advertised, tool.Function.Name)
			}
			assert.Contains(t, advertised, "compact")
		})
	}
}

func TestCompleteChatAddSymbolUsesReferencedSymbols(t *testing.T) {
	h := &aiEditorHandler{parser: &symbolsParser{
		symbols: []string{"pkg.A", "pkg.B", "pkg.A"},
	}}

	it, err := h.Complete(t.Context(), commandAddSymbol, []string{"pkg"})
	require.NoError(t, err)
	defer func() { require.NoError(t, it.Close()) }()

	var got []string
	for {
		symbol, ok := it.Next(t.Context())
		if !ok {
			break
		}
		got = append(got, symbol)
	}
	require.NoError(t, it.Err())
	assert.Equal(t, []string{"pkg.A", "pkg.B"}, got)
}

func TestOpenChatTabUsesDialogueIDAsLabel(t *testing.T) {
	const dialogueID = "rolling-fox"
	uri, err := getModelUri(dialogueID, "gpt-5")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(uri.String(), "rune-agent://"),
		"precondition: URI must use the rune-agent scheme")

	wm := &recordingWindowManager{}
	_, err = openChatTab(wm, uri, dialogueID, nil)
	require.NoError(t, err)

	assert.Equal(t, dialogueID, wm.gotName,
		"visible tab label must be the dialogue ID, not the internal URI")
	assert.False(t, strings.HasPrefix(wm.gotName, "rune-agent://"),
		"tab label must not be the internal rune-agent:// URI")
	assert.Equal(t, uri, wm.gotURI, "URI must still be passed unchanged as tab identity")
}

func TestHandleChatRejectsAlreadyOpenDialogue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const dialogueID = "RUNE-256"
	svc := llmtest.New([]llmapi.ModelEntry{{Provider: "test", Name: "test-model"}})
	wm := &recordingWindowManager{}
	fs := nopFileSystem{}
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   "test-model",
		dialogueStore:  newMemDialogueStore(),
		wm:             wm,
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}

	require.NoError(t, h.handleChat(textapi.Command{Args: []string{dialogueID}}))
	require.NotNil(t, wm.gotHandler)
	t.Cleanup(func() {
		require.NoError(t, wm.gotHandler.Close())
		wm.gotHandler = nil
	})
	assert.Equal(t, dialogueID, wm.gotName)

	err := h.handleChat(textapi.Command{Args: []string{dialogueID}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent chat "RUNE-256" is already open`)
}

func TestHandleChatTracksTabURIForActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const dialogueID = "rolling-fox"
	svc := llmtest.New([]llmapi.ModelEntry{{Provider: "test", Name: "test-model"}})
	wm := &recordingWindowManager{}
	fs := nopFileSystem{}
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   "test-model",
		dialogueStore:  newMemDialogueStore(),
		wm:             wm,
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}

	require.NoError(t, h.handleChat(textapi.Command{Args: []string{dialogueID}}))
	require.NotNil(t, wm.gotHandler)
	t.Cleanup(func() { require.NoError(t, wm.gotHandler.Close()) })

	v, ok := h.openChats.Load(dialogueID)
	require.True(t, ok)
	assert.Equal(t, wm.gotURI, v.(syncComponent).tab.uri)
}

func TestOpenResourceResumesChat(t *testing.T) {
	const dialogueID = "rolling-fox"
	chatURI := func(model string) workspaceapi.URI {
		uri, err := getModelUri(dialogueID, model)
		require.NoError(t, err)
		return uri
	}
	tests := []struct {
		name      string
		uri       string
		stored    *dialoguemanager.Dialogue
		wantErr   string
		wantModel string
	}{
		{
			name:    "other scheme",
			uri:     "file:///rolling-fox",
			wantErr: "is not an agent chat",
		},
		{
			name:    "no dialogue id",
			uri:     "rune-agent://test-model",
			wantErr: "is not an agent chat",
		},
		{
			name:      "dialogue that was never stored uses the default model",
			uri:       chatURI("other-model").String(),
			wantModel: "test-model",
		},
		{
			name:      "stored model is resumed",
			uri:       chatURI("test-model").String(),
			stored:    &dialoguemanager.Dialogue{ID: dialogueID, Model: "other-model"},
			wantModel: "other-model",
		},
		{
			name:      "unavailable stored model falls back to the default",
			uri:       chatURI("gone-model").String(),
			stored:    &dialoguemanager.Dialogue{ID: dialogueID, Model: "gone-model"},
			wantModel: "test-model",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			store := newMemDialogueStore()
			if tt.stored != nil {
				require.NoError(t, store.Create(ctx, *tt.stored))
			}
			svc := llmtest.New([]llmapi.ModelEntry{
				{Provider: "test", Name: "test-model"},
				{Provider: "test", Name: "other-model"},
			})
			wm := &recordingWindowManager{}
			fs := nopFileSystem{}
			h := &aiEditorHandler{
				ctx:            ctx,
				llmSvc:         svc,
				defaultModel:   "test-model",
				dialogueStore:  store,
				wm:             wm,
				n:              stubNotifications{},
				p:              term.NopInterrupter(),
				config:         configedit.NopConfig(),
				skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
				toolRegistry:   agent.NewRegistry(),
				agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
				cwd:            dirURI(""),
				fs:             fs,
				memoryDataPath: t.TempDir(),
			}
			uri, err := workspaceapi.ParseURI(tt.uri)
			require.NoError(t, err)

			content, err := h.OpenResource(ctx, uri)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, content)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, content)
			assert.Nil(t, wm.gotHandler, "the tab is the host's, not the extension's")
			assert.Zero(t, wm.setContentCalls, "placement is the host's")
			v, ok := h.openChats.Load(dialogueID)
			require.True(t, ok)
			assert.Equal(t, uri, v.(syncComponent).tab.uri,
				"the chat must go by the URI the host asked for, whatever the model")
			a, ok := h.openChatAgents.Load(dialogueID)
			require.True(t, ok)
			assert.Equal(t, tt.wantModel, a.(*agent.Agent).Model())

			_, err = h.OpenResource(ctx, uri)
			require.ErrorContains(t, err, "is already open")

			require.NoError(t, content.Close())
			_, ok = h.openChats.Load(dialogueID)
			assert.False(t, ok, "closing the content must end the chat")
		})
	}
}

func newCompleteResourceHandler(t *testing.T, store dialoguemanager.Store) *aiEditorHandler {
	h := &aiEditorHandler{
		defaultModel:  "test-model",
		dialogueStore: store,
		gitID: gitIdentity{worktrees: map[string]string{
			"file:///my/workspace":  "workspace",
			"file:///my/worktree-b": "worktree-b",
		}},
	}
	h.cwd, _ = workspaceapi.ParseURI("file:///my/workspace")
	tabURI, err := workspaceapi.ParseURI("rune-agent://test-model/open-chat")
	require.NoError(t, err)
	h.openChats.Store("open-chat", syncComponent{tab: newTabStatusReporter(nil, tabURI, nil)})
	h.openChats.Store("opening-chat", struct{}{})
	return h
}

// completeResourceDialogues are in the order the store lists them.
var completeResourceDialogues = []dialoguemanager.DialogueHeader{
	{ID: "other-chat", WorkspaceURI: "ssh://host/other/project", Model: "test-model"},
	{ID: "sub-agent", WorkspaceURI: "file:///my/workspace", SubAgent: true},
	{ID: "open-chat", WorkspaceURI: "file:///my/workspace", Model: "other-model"},
	{ID: "sibling-chat", WorkspaceURI: "file:///my/worktree-b", Model: "test-model"},
	{ID: "opening-chat", WorkspaceURI: "file:///my/workspace", Model: "other-model"},
	{ID: "stored-model", WorkspaceURI: "file:///my/workspace", Model: "hf.co/org/model:tag"},
	{ID: "no-model"},
}

func TestCompleteResourceOffersChatURIs(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{
			name:   "chats in store order",
			prefix: "rune-agent://",
			want: []string{
				"rune-agent://test-model/open-chat",
				"rune-agent://test-model/sibling-chat",
				"rune-agent://other-model/opening-chat",
				"rune-agent://hf.co_org_model_tag/stored-model",
				"rune-agent://test-model/no-model",
			},
		},
		{
			name:   "filtered by prefix",
			prefix: "rune-agent://test-model/s",
			want:   []string{"rune-agent://test-model/sibling-chat"},
		},
		{
			name:   "open chat is offered as its tab only",
			prefix: "rune-agent://other-model/open-chat",
			want:   []string{},
		},
		{
			name:   "no match",
			prefix: "rune-agent://missing",
			want:   []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeListStore{dialogues: completeResourceDialogues}
			h := newCompleteResourceHandler(t, store)
			it, err := h.CompleteResource(t.Context(), tt.prefix)
			require.NoError(t, err)
			got, err := iterator.ToSlice(t.Context(), it)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.True(t, store.closed)
		})
	}
}

func TestCompleteResourceStreamsChats(t *testing.T) {
	store := &fakeListStore{dialogues: completeResourceDialogues}
	h := newCompleteResourceHandler(t, store)
	it, err := h.CompleteResource(t.Context(), "rune-agent://")
	require.NoError(t, err)
	assert.Zero(t, store.pulled, "nothing is listed before it is asked for")

	uri, ok := it.Next(t.Context())
	require.True(t, ok)
	assert.Equal(t, "rune-agent://test-model/open-chat", uri)
	assert.Equal(t, 3, store.pulled, "only the dialogues up to the first chat are listed")

	require.NoError(t, it.Close())
	assert.True(t, store.closed)
}

func TestCompleteResourceListFailure(t *testing.T) {
	h := newCompleteResourceHandler(t, &fakeListStore{err: errors.New("list failed")})
	it, err := h.CompleteResource(t.Context(), "rune-agent://")
	require.ErrorContains(t, err, "list failed")
	assert.Nil(t, it)
}

func TestChatCompactUsesChatMaxOutputTokens(t *testing.T) {
	const dialogueID = "rolling-fox"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	store := newMemDialogueStore()
	require.NoError(t, store.Create(ctx, dialoguemanager.Dialogue{
		ID:    dialogueID,
		Model: "test-model",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "system"},
			{Role: llmapi.RoleUser, Content: "question"},
			{Role: llmapi.RoleAssistant, Content: "answer"},
		},
	}))
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{Chunks: []string{"<summary>done</summary>"}},
	)
	fs := nopFileSystem{}
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         svc,
		defaultModel:   "test-model",
		dialogueStore:  store,
		wm:             &recordingWindowManager{},
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}
	c, err := h.newChat(textapi.Command{Args: []string{dialogueID}}, workspaceapi.URI{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.content.Close()) })

	ag, ok := h.openChatAgents.Load(dialogueID)
	require.True(t, ok)
	ag.(*agent.Agent).SetMaxOutputTokens(50_000)

	c.content.Resize(frameWidth, frameHeight)
	keys, err := term.ParseKeys("/compact<space>test/test-model<enter>")
	require.NoError(t, err)
	for _, k := range keys {
		c.content.Handle(term.Event{Type: term.EventKey, Mod: k.Mod, Key: k.Key, Ch: k.Ch})
	}

	require.Eventually(t, func() bool { return len(svc.Requests()) > 0 },
		5*time.Second, 10*time.Millisecond, "/compact never reached the LLM")
	assert.Equal(t, 50_000, svc.Requests()[0].Request.MaxOutputTokens)
}

func TestConsoleMaxTokensUpdatesOpenChats(t *testing.T) {
	const dialogueID = "rolling-fox"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	layout, err := dialoguetui.ParseStatusBarLayout(`{{ .Model }}  max {{ .MaxTokens }}`)
	require.NoError(t, err)
	entry := llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000}
	svc := llmtest.New([]llmapi.ModelEntry{entry})
	fs := nopFileSystem{}
	store := newMemDialogueStore()
	skillReg := skills.NewRegistry(fs, dirURI(""), nil, nil)
	h := &aiEditorHandler{
		ctx:          ctx,
		llmSvc:       svc,
		defaultModel: "test-model",
		cfg: dialoguetui.ComponentConfig{
			StatusBar: dialoguetui.StatusBarConfig{Enabled: true, Layout: layout},
		},
		dialogueStore:  store,
		wm:             &recordingWindowManager{},
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		skillRegistry:  skillReg,
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
		queryAgent: agent.NewAgent(svc, agent.NewRegistry(), skillReg, store,
			agent.NoMemory(), agent.Config{Model: entry}),
	}
	c, err := h.newChat(textapi.Command{Args: []string{dialogueID}}, workspaceapi.URI{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.content.Close()) })

	handlertest.RunHandlerSequence(t, c.content, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		Expected: frame(
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
			"test-model",
		),
	}})

	it, err := h.newAgentShell().HandleCommand(ctx,
		repl.Command{Name: "max_tokens", Args: []string{"64000"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	require.NoError(t, it.Close())

	handlertest.RunHandlerSequence(t, c.content, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		Expected: frame(
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
			"test-model  max 64k",
		),
	}})

	assert.Equal(t, 64000, h.queryAgent.MaxOutputTokens())
	ag, ok := h.openChatAgents.Load(dialogueID)
	require.True(t, ok)
	assert.Equal(t, 64000, ag.(*agent.Agent).MaxOutputTokens())
}

func TestResolveMaxTokens(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		want int
	}{
		{name: "unset uses the provider default", cfg: map[string]any{}, want: 0},
		{name: "persisted value", cfg: map[string]any{"max_tokens": 200_000}, want: 200_000},
		{name: "non-positive uses the provider default", cfg: map[string]any{"max_tokens": -1}, want: 0},
		{name: "wrong type uses the provider default", cfg: map[string]any{"max_tokens": "lots"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveMaxTokens(config.MapConfig(tt.cfg)))
		})
	}
}

func TestE2ECtrlCDismissesSelectionPrompt(t *testing.T) {
	args := mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question": "Pick one", "header": "Choice",
			"options": []map[string]any{
				{"label": "A", "description": ""},
				{"label": "B", "description": ""},
			},
			"multiSelect": false,
		}},
	})
	h := newPromptHandler(t, promptHandlerOpts{toolArgs: args})
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			// Submit the user message; the agent loop emits the
			// tool call and the selection prompt becomes visible.
			InputSequence: "hi<enter>",
			Expected: frame(
				"hi",
				"Pick one                        [Choice]",
				blanks(),
				"> A",
				"  B",
				"  Other",
				"      None of the above",
				"   ┌───────────────────────────────┐    ",
				"   │                               │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			// Ctrl-C through the wrapped chat handler must clear
			// the prompt and return focus to the empty input box.
			InputSequence: "<c-c>",
			Expected: frame(
				"hi",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

func TestE2EMultiSelectSpaceToggles(t *testing.T) {
	args := mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question": "Pick some", "header": "Choice",
			"options": []map[string]any{
				{"label": "A", "description": ""},
				{"label": "B", "description": ""},
			},
			"multiSelect": true,
		}},
	})
	h := newPromptHandler(t, promptHandlerOpts{toolArgs: args})
	unchecked := frame(
		"hi",
		"Pick some                       [Choice]",
		blanks(),
		">[ ] A",
		"[ ] B",
		"[ ] Other",
		"      None of the above",
		"   ┌───────────────────────────────┐    ",
		"   │                               │    ",
		"   └───────────────────────────────┘    ",
	)
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			// Submit the user message; the agent loop emits the
			// tool call and the multiSelect prompt becomes visible.
			InputSequence: "hi<enter>",
			Expected:      unchecked,
		},
		{
			// Enter with nothing checked is ignored: the prompt
			// stays exactly as it was.
			InputSequence: "<enter>",
			Expected:      unchecked,
		},
		{
			// Space ticks the box under the cursor.
			InputSequence: "<space>",
			Expected: frame(
				"hi",
				"Pick some                       [Choice]",
				blanks(),
				">[x] A",
				"[ ] B",
				"[ ] Other",
				"      None of the above",
				"   ┌───────────────────────────────┐    ",
				"   │                               │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			// Enter submits the checked option; the prompt is
			// cleared and focus returns to the empty input box.
			InputSequence: "<enter>",
			Expected: frame(
				"hi",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

func TestE2ENoLSPLanguageDisablesAutoDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		diagErr string
	}{
		{"not supported yet", "python language LSP is not supported yet"},
		{"server not running", "no language server: server python not running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			svc := llmtest.New(
				[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
				applyPatchResponse("call-edit-1", "a.py"),
				applyPatchResponse("call-edit-2", "b.py"),
				llmtest.Response{
					Chunks:       []string{"done"},
					FinishReason: llmapi.FinishReasonStop,
				},
			)

			lsp := stubLSP{
				diagnosticFn: func(semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
					return semanticapi.DocumentDiagnosticReport{}, errors.New(tc.diagErr)
				},
			}

			h := agentE2EHandler(t, svc, dir, lsp)

			handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
				InputSequence: "hi<enter>",
				Expected: frame(
					"hi",
					blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
					"   ┌───────────────────────────────┐    ",
					"   │▐                              │    ",
					"   └───────────────────────────────┘    ",
				),
			}})

			// Wait for the full scripted loop (3 CreateCompletion calls).
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if svc.CallCount() >= 3 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			require.GreaterOrEqualf(t, svc.CallCount(), 3,
				"expected scripted agent loop to issue all 3 CreateCompletion calls, got %d",
				svc.CallCount())

			requests := svc.Requests()
			require.GreaterOrEqual(t, len(requests), 3)

			// request[1] carries the messages produced after turn 1: the
			// first apply_patch and its auto-injected check_file_errors.
			firstTurn := requests[1].Request.Messages
			assert.Equal(t, 1, assistantSyntheticDiagCalls(firstTurn),
				"first .py edit must auto-inject check_file_errors")
			diagResult, ok := findToolResult(firstTurn, "auto-diag-call-edit-1")
			require.True(t, ok, "first turn must carry the synthetic diagnostic result")
			assert.Contains(t, diagResult, tc.diagErr,
				"synthetic diagnostic must surface the no-LSP error once")

			// request[2] carries the messages produced after turn 2: the
			// second apply_patch must have NO synthetic check_file_errors,
			// because python was disabled for the session on turn 1.
			secondTurn := requests[2].Request.Messages
			assert.Equal(t, 1, assistantSyntheticDiagCalls(secondTurn),
				"second .py edit must not re-inject check_file_errors for a disabled language")
			_, ok = findToolResult(secondTurn, "auto-diag-call-edit-2")
			assert.False(t, ok,
				"second edit must not produce a synthetic diagnostic for a disabled language")
		})
	}
}

func TestE2EUTF8SafetyAcrossAgentLoop(t *testing.T) {
	dir := t.TempDir()

	// utf8.txt: valid multi-byte text containing the literal "needle"
	// so grep_files has at least one positive hit.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "utf8.txt"),
		[]byte("héllo · 世界\nfind the needle in the haystack\n"), 0o644))
	// latin1.log: ASCII with one stray 0xff byte sliced into a line.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "latin1.log"),
		[]byte("normal line\nbad byte here: \xff oops\nmore text\n"), 0o644))
	// bin.dat: looks binary by both heuristics (NUL in first 8 KiB)
	// and embeds the same "needle" token between binary bytes so the
	// grep filter is the only thing keeping it out of results.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin.dat"),
		[]byte("\x7fELF\x02\x01\x01\x00\x00\x00needle\x00data\x01\x02"), 0o644))

	// Scripted LLM: four tool-calls then stop. The arguments include a
	// stray invalid UTF-8 byte in a path field on purpose so the
	// wire-level sanitiser in agent.go (Layer 1) has to scrub it
	// before CreateCompletion is invoked on the next turn.
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-read-utf8",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"utf8.txt","offset":0,"limit":0}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-read-latin1",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name: "read_file",
					// Embed a stray 0xff byte to exercise Layer 1's
					// tool-call Arguments sanitisation.
					Arguments: "{\"path\":\"latin1.log\",\"offset\":0,\"limit\":0,\"_pad\":\"\xff\"}",
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-read-bin",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"bin.dat","offset":0,"limit":0}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-grep",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "grep_files",
					Arguments: `{"pattern":"needle"}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := utf8E2EHandler(t, svc, dir)

	// Drive the handler through its public interface. The user types
	// "hi" and presses Enter; the scripted agent loop runs all four
	// tool calls and the final stop. We only assert on the final
	// frame (assistant text "done" appears) — the per-tool assertions
	// run after the sequence against the captured request log.
	// The chat area shows the user's message at the top; the
	// assistant's "done" reply and tool-spinner lines are written
	// into the dialogue component asynchronously by the agent loop
	// and may not be flushed by the time this frame is captured.
	// The RUNE-179 assertions below run after polling for the full
	// scripted sequence to complete.
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// Give the agent loop a final moment to flush the post-tool
	// CreateCompletion calls into the recorder. The promptFlusher
	// settle window covers most of this, but the stop reply itself
	// arrives after the last interrupt so we wait once more.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.CallCount() >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqualf(t, svc.CallCount(), 5,
		"expected scripted agent loop to issue all 5 CreateCompletion calls, got %d", svc.CallCount())

	requests := svc.Requests()

	// (1) Layer 1: every outgoing string field is valid UTF-8.
	for k, r := range requests {
		for i, m := range r.Request.Messages {
			assert.Truef(t, utf8.ValidString(m.Content),
				"request %d message %d Content has invalid UTF-8: %q", k, i, m.Content)
			assert.Truef(t, utf8.ValidString(m.ReasoningContent),
				"request %d message %d ReasoningContent has invalid UTF-8", k, i)
			for j, tc := range m.ToolCalls {
				assert.Truef(t, utf8.ValidString(tc.Function.Arguments),
					"request %d message %d toolcall %d Arguments has invalid UTF-8: %q",
					k, i, j, tc.Function.Arguments)
				assert.Truef(t, utf8.ValidString(tc.Function.Name),
					"request %d message %d toolcall %d Name has invalid UTF-8", k, i, j)
			}
		}
	}

	// The Nth scripted response is processed during the Nth call; the
	// next call (N+1) is the first one whose Messages slice carries
	// the tool-role result from that response. So the message log on
	// request[i+1] is where we can inspect tool result i.
	require.GreaterOrEqual(t, len(requests), 5)
	utf8Msgs := requests[1].Request.Messages
	latin1Msgs := requests[2].Request.Messages
	binMsgs := requests[3].Request.Messages
	grepMsgs := requests[4].Request.Messages

	// (2) Layer 3: binary file → BinaryStub with bash hint.
	binResult, ok := findToolResult(binMsgs, "call-read-bin")
	require.True(t, ok, "expected tool result for call-read-bin in request 3")
	header := regexp.MustCompile(`^<binary file: bin\.dat, \d+ bytes, sha256=[0-9a-f]{64}`)
	assert.Regexp(t, header, binResult, "binary file result must start with the stub header")
	assert.Contains(t, binResult, "use bash",
		"binary stub must instruct the model to fall back to bash")
	assert.Contains(t, binResult, "xxd",
		"binary stub must mention xxd / hexdump / strings as inspection options")

	// (3) Layer 3: latin-1 file → U+FFFD marker, bad byte replaced.
	latin1Result, ok := findToolResult(latin1Msgs, "call-read-latin1")
	require.True(t, ok, "expected tool result for call-read-latin1 in request 2")
	assert.Truef(t, utf8.ValidString(latin1Result),
		"latin-1 tool result must be valid UTF-8: %q", latin1Result)
	assert.Contains(t, latin1Result, "\ufffd",
		"latin-1 tool result must replace bad bytes with U+FFFD")
	assert.Contains(t, latin1Result, "(1 invalid UTF-8 byte replaced with U+FFFD)",
		"latin-1 tool result must carry the byte-count marker")

	// (4) Layer 3: valid UTF-8 file → verbatim, no marker, no stub.
	utf8Result, ok := findToolResult(utf8Msgs, "call-read-utf8")
	require.True(t, ok, "expected tool result for call-read-utf8 in request 1")
	assert.Contains(t, utf8Result, "héllo · 世界",
		"utf-8 tool result must round-trip multi-byte characters")
	assert.NotContains(t, utf8Result, "invalid UTF-8",
		"valid utf-8 file must not be tagged with a sanitisation marker")
	assert.NotContains(t, utf8Result, "<binary file:",
		"valid utf-8 file must not be tagged as binary")

	// (5) Layer 4: grep_files matches the utf-8 text file but skips
	// the binary blob even though both contain the literal "needle".
	grepResult, ok := findToolResult(grepMsgs, "call-grep")
	require.True(t, ok, "expected tool result for call-grep in request 4")
	assert.Contains(t, grepResult, "utf8.txt", "grep must find the text file")
	assert.NotContains(t, grepResult, "bin.dat",
		"grep must skip binary files even when their bytes contain the pattern")
}

func TestE2EPauseTurnResumesAgentLoop(t *testing.T) {
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			Chunks:       []string{"thinking out loud"},
			FinishReason: llmapi.FinishReasonPause,
		},
		llmtest.Response{
			Chunks:       []string{"final answer"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := stopReasonE2EHandler(t, svc)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		// The paused turn's partial text ("thinking out loud") and the
		// resumed turn's text ("final answer") render as one continuous
		// assistant message, proving the loop resumed in place.
		Expected: frame(
			"hi",
			"thinking out loudfinal answer",
			blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// The pause must trigger a second CreateCompletion with no
	// intervening user turn, whose request re-sends the partial
	// assistant message so the model can continue where it paused.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		requests := svc.Requests()
		require.GreaterOrEqual(c, len(requests), 2)
		var foundPartial bool
		for _, m := range requests[1].Request.Messages {
			if m.Role == llmapi.RoleAssistant && strings.Contains(m.Content, "thinking out loud") {
				foundPartial = true
			}
		}
		assert.True(c, foundPartial,
			"resume request must re-send the partial assistant message from the paused turn")
	}, 3*time.Second, 10*time.Millisecond)
}

func TestE2ERefusalTerminatesCleanly(t *testing.T) {
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			Chunks:       []string{"I can't help with that"},
			FinishReason: llmapi.FinishReasonRefusal,
		},
	)

	h := stopReasonE2EHandler(t, svc)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		// The refusal renders the partial assistant text followed by
		// the distinct refusal banner from the EventRefusal branch.
		Expected: frame(
			"hi",
			"I can't help with that",
			blanks(),
			"The model declined to continue with",
			"this request.",
			blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// The refusal banner above only renders after the single
	// completion finished and EventRefusal was processed, so the turn
	// is already terminal here. A resume would require a second
	// completion; assert it never happens (and stays at one).
	assert.Never(t, func() bool { return svc.CallCount() != 1 },
		300*time.Millisecond, 10*time.Millisecond,
		"refusal must terminate the turn without resuming the agent loop")
}

func TestE2EEmptyToolResultIsNeverSentEmpty(t *testing.T) {
	dir := t.TempDir()

	// `touch` writes nothing to stdout/stderr and matches none of the
	// bashToolHint rules, so the bash tool returns ToolResult{Content: ""}.
	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-empty",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "bash",
					Arguments: `{"command":"touch empty_marker.txt"}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := utf8E2EHandler(t, svc, dir)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	// Wait for both scripted calls: the tool turn and the stop reply.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.CallCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqualf(t, svc.CallCount(), 2,
		"expected the scripted agent loop to issue both CreateCompletion calls, got %d", svc.CallCount())

	// The tool result for call-empty is replayed on the second request.
	requests := svc.Requests()
	require.GreaterOrEqual(t, len(requests), 2)
	result, ok := findToolResult(requests[1].Request.Messages, "call-empty")
	require.True(t, ok, "expected tool result for call-empty in request 1")
	assert.NotEmpty(t, result,
		"empty tool output must not be replayed as an empty tool_result; "+
			"Anthropic rejects it with \"text content blocks must be non-empty\"")
}

func TestE2ECtrlCDismissesFreeFormPrompt(t *testing.T) {
	args := mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question":    "What is your name?",
			"header":      "Name",
			"options":     []map[string]any{},
			"multiSelect": false,
		}},
	})
	h := newPromptHandler(t, promptHandlerOpts{toolArgs: args})
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			InputSequence: "hi<enter>",
			Expected: frame(
				"hi",
				"Name: What is your name?",
				blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			InputSequence: "Ali",
			Expected: frame(
				"hi",
				"Name: What is your name?",
				blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │Ali▐                           │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			InputSequence: "<c-c>",
			Expected: frame(
				"hi",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │Ali▐                           │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

func TestE2ECtrlCDismissesRequiresInputPrompt(t *testing.T) {
	h := newPromptHandler(t, promptHandlerOpts{
		extraPrompt: &dialoguetui.MessageEvent{
			Type:         dialoguetui.MessageEventPrompt,
			PromptTitle:  "Choose",
			PromptHeader: "Choice",
			PromptOptions: []dialoguetui.PromptEventOption{
				{Label: "Other", RequiresInput: true},
			},
			PromptResult: make(chan []string, 1),
		},
	})
	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{
		{
			// Enter selects "Other" which has RequiresInput=true,
			// switching the dialogue handler into PromptInputMode.
			InputSequence: "<enter>",
			Expected: frame(
				"▐ype your feedback and press Enter to   ",
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │                               │    ",
				"   └───────────────────────────────┘    ",
			),
		},
		{
			// Ctrl-C in PromptInputMode must dismiss the whole
			// prompt, not just exit text input.
			InputSequence: "<c-c>",
			Expected: frame(
				blanks(), blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
				"   ┌───────────────────────────────┐    ",
				"   │▐                              │    ",
				"   └───────────────────────────────┘    ",
			),
		},
	})
}

func TestE2ESubAgentInheritsQualifiedModel(t *testing.T) {
	const (
		modelName = "claude-opus-4-8"
		provider  = "anthropic"
	)
	// Two providers expose the same model name, so a bare-name lookup
	// is ambiguous; only a provider-qualified argument resolves.
	catalog := []llmapi.ModelEntry{
		{Provider: "anthropic", Name: modelName, ContextWindow: 128_000},
		{Provider: "claude", Name: modelName, ContextWindow: 128_000},
	}

	// Parent LLM: one agent-tool call (inherits the model), then stop.
	parentSvc := llmtest.New(catalog,
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-spawn",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "agent",
					Arguments: `{"description":"do work","prompt":"investigate"}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	// Sub-agent LLM: a single stop reply. Wrapped in a strict service
	// so the spawner's llmarg.Resolve must disambiguate by provider.
	subSvc := &strictProviderService{
		Service: llmtest.New(catalog, llmtest.Response{
			Chunks:       []string{"sub done"},
			FinishReason: llmapi.FinishReasonStop,
		}),
		models: catalog,
	}

	var (
		factoryMu     sync.Mutex
		factoryModels []string
		factoryErr    error
	)
	serviceFactory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
		entry, err := llmarg.Resolve(context.Background(), subSvc, model)
		factoryMu.Lock()
		factoryModels = append(factoryModels, model)
		if err != nil {
			factoryErr = err
		}
		factoryMu.Unlock()
		if err != nil {
			return nil, llmapi.ModelEntry{}, err
		}
		return subSvc, entry, nil
	}

	h, parent := subAgentSpawnE2EHandler(t, parentSvc, serviceFactory,
		llmapi.ModelEntry{Provider: "codex", Name: "gpt-5.6-sol", ContextWindow: 128_000})
	parent.SwapService(parentSvc,
		llmapi.ModelEntry{Provider: provider, Name: modelName, ContextWindow: 128_000})

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			// "✓" is one display column but three bytes; frame's
			// byte-based padding would under-pad, so pad this row
			// explicitly to the 40-column frame width.
			"✓ agent do work"+strings.Repeat(" ", frameWidth-len([]rune("✓ agent do work"))),
			"sub done",
			"done",
			blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		require.NotEmpty(c, factoryModels,
			"spawner service factory should have been invoked for the sub-agent")
	}, 3*time.Second, 10*time.Millisecond)

	factoryMu.Lock()
	defer factoryMu.Unlock()
	require.NoError(t, factoryErr,
		"sub-agent model must resolve without an ambiguity error")
	for _, m := range factoryModels {
		assert.Equalf(t, provider+"/"+modelName, m,
			"sub-agent must inherit the provider-qualified model, got %q", m)
	}
}

func TestPlanSkillSpawnInheritsQualifiedModel(t *testing.T) {
	const (
		modelName = "claude-opus-4-8"
		provider  = "claude"
	)
	catalog := []llmapi.ModelEntry{
		{Provider: "anthropic", Name: modelName, ContextWindow: 128_000},
		{Provider: "claude", Name: modelName, ContextWindow: 128_000},
	}

	// Register a "plan" agent-type skill on disk.
	skillDir := t.TempDir()
	planDir := filepath.Join(skillDir, "plan")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "SKILL.md"), []byte(
		"---\nname: plan\ndescription: Plan things\ntype: agent\n---\nYou are a planner.",
	), 0o644))
	skillReg := skills.NewRegistry(testLocalFS{root: "/"}, dirURI(""), []string{planDir}, nil)
	planSkill, ok := skillReg.Get("plan")
	require.True(t, ok, "plan skill must load")
	require.Equal(t, "agent", planSkill.Type)

	subSvc := &strictProviderService{
		Service: llmtest.New(catalog, llmtest.Response{
			Chunks:       []string{"sub done"},
			FinishReason: llmapi.FinishReasonStop,
		}),
		models: catalog,
	}
	var (
		factoryMu     sync.Mutex
		factoryModels []string
		factoryErr    error
	)
	serviceFactory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
		entry, err := llmarg.Resolve(context.Background(), subSvc, model)
		factoryMu.Lock()
		factoryModels = append(factoryModels, model)
		if err != nil {
			factoryErr = err
		}
		factoryMu.Unlock()
		if err != nil {
			return nil, llmapi.ModelEntry{}, err
		}
		return subSvc, entry, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	store := newMemDialogueStore()
	const dialogueID = "test-dialogue"
	cfg := agent.NewConfig([]agent.Definition{
		{ID: "default", Name: "Default Agent", AllowAny: true},
	})
	spawner := agent.NewGoroutineSpawner(
		store, serviceFactory, cfg, skillReg, agent.NoMemory(), "",
		dialogueID, "default", dirURI(""), noopAgentPrompter{},
	)
	registry := agent.NewRegistry()
	spawner.SetRegistry(registry)

	parentSvc := llmtest.New(catalog)
	ag := agent.NewAgent(parentSvc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        llmapi.ModelEntry{Provider: provider, Name: modelName, ContextWindow: 128_000},
		SessionKey:   dialogueID,
		AgentID:      "default",
	})

	tx := make(chan dialoguetui.MessageEvent, 64)
	rx := make(chan completionRequest, 1)
	childEvents := make(chan agent.ChildEvent, 64)
	rx <- completionRequest{displayText: "plan it", modelText: "plan it", skillName: "plan", ctx: ctx}

	done := make(chan struct{})
	go func() {
		defer close(done)
		createAgentCompletions(ctx, cancel, tx, rx, ag, spawner,
			childEvents, skillReg, dialogueID,
			newStatusBarSyncComponent(),
			nil, store)
	}()

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		require.NotEmpty(c, factoryModels,
			"spawner service factory should have been invoked for the plan sub-agent")
	}, 3*time.Second, 10*time.Millisecond)

	cancel()
	<-done

	factoryMu.Lock()
	defer factoryMu.Unlock()
	require.NoError(t, factoryErr,
		"plan sub-agent model must resolve without an ambiguity error")
	for _, m := range factoryModels {
		assert.Equalf(t, provider+"/"+modelName, m,
			"plan sub-agent must inherit the provider-qualified model, got %q", m)
	}
}

func TestE2EMaxTokensRejectedAboveModelCeiling(t *testing.T) {
	model := llmapi.ModelEntry{
		Provider: "anthropic", Name: "claude-sonnet-4-5", ContextWindow: 200_000,
	}
	h, ag := maxTokensE2EHandler(t, model)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "/max_tokens<space>128000<enter>",
		Expected: frame(
			"/max_tokens 128000",
			"! claude-sonnet-4-5 supports at most",
			"64000 max output tokens; 128000 is too",
			"large",
			blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	assert.Equal(t, 0, ag.MaxOutputTokens(),
		"rejected /max_tokens must not apply the session override")
}

func TestE2ESearchContentSkipsSwapFiles(t *testing.T) {
	dir := t.TempDir()

	// Both files contain the literal "needle". The swap file additionally
	// carries a sentinel so we can assert its contents never reach the LLM.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"),
		[]byte("find the needle here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".tracked.txt.rswp"),
		[]byte("needle SWAPSENTINEL must not leak\n"), 0o644))

	svc := llmtest.New(
		[]llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}},
		llmtest.Response{
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call-search",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "search_content",
					Arguments: `{"pattern":"needle","path":null,"include":null}`,
				},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{
			Chunks:       []string{"done"},
			FinishReason: llmapi.FinishReasonStop,
		},
	)

	h := utf8E2EHandler(t, svc, dir)

	handlertest.RunHandlerSequence(t, h, frameWidth, frameHeight, []handlertest.SequenceTestCase{{
		InputSequence: "hi<enter>",
		Expected: frame(
			"hi",
			blanks(), blanks(), blanks(), blanks(), blanks(), blanks(),
			"   ┌───────────────────────────────┐    ",
			"   │▐                              │    ",
			"   └───────────────────────────────┘    ",
		),
	}})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.CallCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqualf(t, svc.CallCount(), 2,
		"expected scripted agent loop to issue both CreateCompletion calls, got %d", svc.CallCount())

	requests := svc.Requests()
	require.GreaterOrEqual(t, len(requests), 2)

	// The tool result from the first response is carried on the message
	// log of the second request.
	searchResult, ok := findToolResult(requests[1].Request.Messages, "call-search")
	require.True(t, ok, "expected tool result for call-search in request 1")

	assert.Contains(t, searchResult, "tracked.txt",
		"search_content must surface the tracked file")
	assert.NotContains(t, searchResult, ".rswp",
		"search_content must skip editor swap files")
	assert.NotContains(t, searchResult, "SWAPSENTINEL",
		"search_content must not leak swap file contents to the model")
}

func TestChatRepeatedReadRequestsReturnToolErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		varied bool
	}{
		{name: "identical section"},
		{name: "different already-read subranges", varied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const repeats = 20
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "sample.txt"),
				[]byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n"), 0o644))
			var responses []llmtest.Response
			for i := range repeats + 2 {
				offset, limit := 2, 6
				if tc.varied && i > 0 {
					offset, limit = 3+i%2, 2
				}
				if i == repeats+1 {
					offset, limit = 8, 1
				}
				responses = append(responses, llmtest.Response{
					ToolCalls: []llmapi.ToolCall{{
						ID: fmt.Sprintf("read-%d", i), Type: llmapi.ToolTypeFunction,
						Function: llmapi.FunctionCall{
							Name:      "read_file",
							Arguments: fmt.Sprintf(`{"path":"sample.txt","offset":%d,"limit":%d}`, offset, limit),
						},
					}},
					FinishReason: llmapi.FinishReasonToolCall,
				})
			}
			responses = append(responses, llmtest.Response{Chunks: []string{"finished normally"}, FinishReason: llmapi.FinishReasonStop})
			model := llmapi.ModelEntry{Provider: "codex", Name: "test-model", ContextWindow: 128_000}
			svc := llmtest.New([]llmapi.ModelEntry{model}, responses...)
			ctx, cancel := context.WithCancel(t.Context())
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
			fs, cwd := testLocalFS{root: dir}, dirURI(dir)
			tools, tracker := agentools.DefaultTools(fs, testLocalExec{}, cwd, nil, agentools.NoIndex(), agentools.Config{}, configedit.NopConfig())
			registry := agent.NewRegistry(tools...)
			registerOpenAITools(registry, fs, cwd, agentools.NoIndex(), tracker, nil, configedit.NopConfig())
			skillReg := skills.NewRegistry(fs, cwd, nil, nil)
			store := newMemDialogueStore()
			ag := agent.NewAgent(svc, registry, skillReg, store, agent.NoMemory(), agent.Config{
				SystemPrompt: "test", Model: model, Workspace: cwd,
			})
			owner := &aiEditorHandler{n: stubNotifications{}, p: interrupter}
			syncComp := syncComponent{mu: mu, comp: comp, h: owner, tab: newTabStatusReporter(nil, workspaceapi.URI{}, owner.n)}
			wrapped, msgRx := owner.wrapDialogueHandler(ctx, syncComp, dhandler, rx, "repeated-reads")
			done := make(chan struct{})
			go debug.CapturePanicReport(func() {
				defer close(done)
				createAgentCompletions(ctx, cancel, tx, msgRx, ag, nopSpawner{},
					make(chan agent.ChildEvent), skillReg, "repeated-reads", syncComp, nil, store)
			})
			t.Cleanup(func() {
				cancel()
				<-done
			})
			h := &promptFlusher{t: t, inner: wrapped, interruptCh: interruptCh, settle: 100 * time.Millisecond}
			const width, height = 100, 200
			h.Resize(width, height)
			keys, err := term.ParseKeys("read<enter>")
			require.NoError(t, err)
			for _, key := range keys {
				h.Handle(term.Event{Type: term.EventKey, Ch: key.Ch, Key: key.Key, Mod: key.Mod})
			}
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, len(responses), svc.CallCount())
				rendered := handlertest.DrawHandler(h, width, height)
				assert.Contains(c, rendered, "finished normally")
				assert.Equal(c, repeats, strings.Count(rendered, "✗ read_file"), rendered)
				assert.Equal(c, 2, strings.Count(rendered, "✓ read_file"), rendered)
				assert.NotContains(c, rendered, "Paused this turn")
			}, 5*time.Second, 10*time.Millisecond)

			requests := svc.Requests()
			require.Len(t, requests, len(responses))
			for i := range repeats + 2 {
				result, ok := findToolResult(requests[i+1].Request.Messages, fmt.Sprintf("read-%d", i))
				require.True(t, ok, "provider must receive the result for read-%d", i)
				switch i {
				case 0:
					assert.Equal(t, "L2: two\nL3: three\nL4: four\nL5: five\nL6: six\nL7: seven\n", result)
				case repeats + 1:
					assert.Equal(t, "L8: eight\n", result)
				default:
					offset, end := 2, 7
					if tc.varied {
						offset = 3 + i%2
						end = offset + 1
					}
					assert.Contains(t, result, fmt.Sprintf("already read lines %d–%d", offset, end))
					assert.Contains(t, result, "sample.txt")
					assert.NotContains(t, result, "L2: two")
					assert.NotContains(t, result, "L4: four")
				}
			}
			for _, message := range requests[len(requests)-1].Request.Messages {
				assert.NotContains(t, message.Content, "Read progress warning")
				if message.Role == llmapi.RoleUser {
					assert.Equal(t, "read", message.Content)
				}
			}
		})
	}
}

func TestHandlerAddToolsIsConcurrentWithReaders(t *testing.T) {
	t.Parallel()

	h := &aiEditorHandler{
		baseTools:    []agent.Tool{stubTool{}},
		toolRegistry: agent.NewRegistry(),
	}
	snapshot := h.tools()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.addTools(stubTool{})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.tools()
		}()
	}
	wg.Wait()

	assert.Len(t, h.tools(), 9)
	assert.Len(t, snapshot, 1,
		"an earlier snapshot must not observe later MCP tools")
}

// stubTool is a minimal agent.Tool for registry bookkeeping tests.
type stubTool struct{ name string }

func (t stubTool) Definition() llmapi.Tool {
	return llmapi.Tool{Function: llmapi.FunctionDefinition{Name: t.name}}
}
func (stubTool) Execute(context.Context, string) agent.ToolResult {
	return agent.ToolResult{}
}
func (stubTool) Summary(string) string         { return "" }
func (stubTool) NeedsDeterministicOrder() bool { return false }

func TestSubscribedChatReceivesLateMCPTools(t *testing.T) {
	t.Parallel()

	h := &aiEditorHandler{toolRegistry: agent.NewRegistry()}
	h.addTools(stubTool{name: "early"})

	snapshot := h.tools()
	chatReg := agent.NewRegistry(snapshot...)

	h.addTools(stubTool{name: "mid"})
	h.subscribeTools("chat-1", len(snapshot), chatReg)
	_, ok := chatReg.Get("mid", "")
	assert.True(t, ok, "delta between snapshot and subscription must be applied")

	h.addTools(stubTool{name: "late"})
	_, ok = chatReg.Get("late", "")
	assert.True(t, ok, "subscribed chats must receive late tools")
	_, ok = h.toolRegistry.Get("late", "")
	assert.True(t, ok, "the persistent registry must receive late tools")

	h.unsubscribeTools("chat-1")
	h.addTools(stubTool{name: "after-close"})
	_, ok = chatReg.Get("after-close", "")
	assert.False(t, ok, "closed chats must not be updated")
}

func TestWarnPendingMCPServersReportsConnecting(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	m := runemcp.NewManagerWithTransport(
		func(string, runemcp.ServerConfig) (gomcp.Transport, error) {
			<-release
			return nil, errors.New("halted")
		})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.ConnectServer(
			context.Background(), "slow-srv", runemcp.ServerConfig{Command: "x"})
	}()
	t.Cleanup(func() { close(release); <-done })
	require.Eventually(t, func() bool {
		servers := m.Servers()
		return len(servers) == 1 && servers[0].Status == runemcp.StatusConnecting
	}, 5*time.Second, 10*time.Millisecond)

	h := &aiEditorHandler{mcpManager: m}
	tx := make(chan dialoguetui.MessageEvent, 1)
	h.warnPendingMCPServers(tx)

	select {
	case ev := <-tx:
		assert.Equal(t, dialoguetui.MessageEventWarning, ev.Type)
		assert.Contains(t, ev.Text, "slow-srv")
	default:
		t.Fatal("expected a pending-MCP warning event")
	}
}

func TestWebBrowserDirIsInWorkspaceHostDataDir(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		err     error
		want    string
		wantErr string
	}{
		{
			name: "local workspace",
			root: "/Users/u/.rune",
			want: "/Users/u/.rune/agent-browser",
		},
		{
			name: "remote workspace server with its own data directory",
			root: "/home/dev/.runedev",
			want: "/home/dev/.runedev/agent-browser",
		},
		{
			name:    "data directory not found",
			err:     os.ErrNotExist,
			wantErr: "resolve the data directory on the workspace host",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := webBrowserDir(context.Background(), fakeInstallRoot{root: tt.root, err: tt.err})

			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHandlerWebBrowser(t *testing.T) {
	const dir = "/home/dev/.runedev/agent-browser"
	tests := []struct {
		name       string
		browserDir string
		lookupErr  error
		want       bool
	}{
		{name: "installed", browserDir: dir, want: true},
		{name: "not installed", browserDir: dir, lookupErr: fs.ErrNotExist},
		{name: "no data directory on the workspace host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &lookupExec{bin: "/opt/bin/agent-browser", err: tt.lookupErr}
			dirs := &dirRecordingFS{}
			h := &aiEditorHandler{
				ctx:        context.Background(),
				executor:   exec,
				fs:         dirs,
				browserDir: tt.browserDir,
			}

			tool := h.webBrowser("chat-1")

			if tt.browserDir == "" {
				assert.Empty(t, exec.lookups(), "nothing to look up without a directory to run in")
			}
			assert.Empty(t, exec.commands(), "looking agent-browser up must not run a command")
			if !tt.want {
				assert.Nil(t, tool)
				return
			}
			require.NotNil(t, tool)

			result := tool.Execute(context.Background(), `{"command":["get","title"]}`)
			require.False(t, result.IsError, result.Content)
			cmds := exec.commands()
			run := cmds[len(cmds)-1]
			assert.Equal(t, "/opt/bin/agent-browser", run.Path)
			assert.Equal(t, dir, run.Dir)
			assert.Contains(t, run.Env, "AGENT_BROWSER_SOCKET_DIR="+dir+"/sockets")
			assert.Equal(t, []string{dir}, dirs.dirs)
		})
	}
}

func TestExtensionDialogueLinkClick(t *testing.T) {
	tests := []struct {
		name        string
		cwd         string
		link        string
		showErr     error
		wantHandled bool
		wantCalls   []string
	}{
		{
			name:        "https is shown",
			link:        "https://example.com/repo",
			wantHandled: true,
			wantCalls:   []string{"show https://example.com/repo"},
		},
		{
			name:        "http is shown",
			link:        "http://example.com/docs?a=1#b",
			wantHandled: true,
			wantCalls:   []string{"show http://example.com/docs?a=1#b"},
		},
		{
			name:        "file is shown",
			link:        "file:///tmp/main.go",
			wantHandled: true,
			wantCalls:   []string{"show file:///tmp/main.go"},
		},
		{
			name:        "relative path uses workspace directory",
			link:        "deploy/reports/README.md",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/project/deploy/reports/README.md"},
		},
		{
			name:        "workspace with trailing slash",
			cwd:         "file:///workspace/project/",
			link:        "./docs/../README.md",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/project/README.md"},
		},
		{
			name:        "parent directory",
			link:        "../README.md",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/README.md"},
		},
		{
			name:        "absolute path keeps filesystem root",
			link:        "/tmp/README.md",
			wantHandled: true,
			wantCalls:   []string{"show file:///tmp/README.md"},
		},
		{
			name:        "escaped path query and fragment are preserved",
			cwd:         "file:///workspace/my%20project",
			link:        "docs/a%20b%23c%2520.md?view=raw#section",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/my%20project/docs/a%20b%23c%2520.md?view=raw#section"},
		},
		{
			name:        "relative path retains ssh authority",
			cwd:         "ssh://alice@example.com:2222/work/project",
			link:        "deploy/reports/README.md",
			wantHandled: true,
			wantCalls:   []string{"show ssh://alice@example.com:2222/work/project/deploy/reports/README.md"},
		},
		{
			name:        "absolute path remains on remote filesystem",
			cwd:         "ssh://alice@example.com:2222/work/project",
			link:        "/tmp/README.md",
			wantHandled: true,
			wantCalls:   []string{"show ssh://alice@example.com:2222/tmp/README.md"},
		},
		{
			name:        "relative path retains rune workspace",
			cwd:         "rune://server/work/project",
			link:        "README.md",
			wantHandled: true,
			wantCalls:   []string{"show rune://server/work/project/README.md"},
		},
		{
			name:        "explicit file link remains local in remote workspace",
			cwd:         "ssh://alice@example.com/work/project",
			link:        "file:///tmp/README.md",
			wantHandled: true,
			wantCalls:   []string{"show file:///tmp/README.md"},
		},
		{
			name:        "failed show notifies",
			link:        "https://example.com/repo",
			showErr:     errors.New("no system browser configured"),
			wantHandled: true,
			wantCalls: []string{
				"show https://example.com/repo",
				"open https://example.com/repo: no system browser configured",
			},
		},
		{name: "anchor is declined", link: "#section"},
		{name: "empty link is declined", link: ""},
		{name: "query without path is declined", link: "?view=raw"},
		{name: "network reference is declined", link: "//example.com/README.md"},
		{name: "mailto is declined", link: "mailto:a@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lock := &dialogueLock{}
			io := &linkClickIO{lock: lock, showErr: tt.showErr}
			cwd := tt.cwd
			if cwd == "" {
				cwd = "file:///workspace/project"
			}
			uri, err := workspaceapi.ParseURI(cwd)
			require.NoError(t, err)
			h := &aiEditorHandler{o: io, n: io, cwd: uri}
			link, err := url.Parse(tt.link)
			require.NoError(t, err)
			original := *link

			// The dialogue holds its lock while it calls OnLinkClick.
			lock.Lock()
			handled := h.dialogueConfig(lock).OnLinkClick(link)
			lock.Unlock()

			assert.Equal(t, tt.wantHandled, handled)
			assert.Equal(t, tt.wantCalls, io.calls)
			assert.Equal(t, original, *link, "the caller's URL must not be mutated")
		})
	}
}

// dialogueLock is a dialogue's sync.Locker. It counts the misuse a
// sync.Mutex would make a fatal error instead, so a test can report it.
type dialogueLock struct {
	mu         sync.Mutex
	held       atomic.Bool
	badUnlocks atomic.Int32
}

func (l *dialogueLock) Lock() {
	l.mu.Lock()
	l.held.Store(true)
}

func (l *dialogueLock) Unlock() {
	if !l.held.Swap(false) {
		l.badUnlocks.Add(1)
		return
	}
	l.mu.Unlock()
}

// linkClickIO is a resource opener and notifications that record each
// call, marking the ones made while lock was held.
type linkClickIO struct {
	stubNotifications
	lock    *dialogueLock
	showErr error
	panics  bool
	calls   []string
}

func (r *linkClickIO) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	r.record("open tab " + uri.String())
	return nil, errors.New("unexpected Open")
}

func (r *linkClickIO) Show(uri workspaceapi.URI) error {
	r.record("show " + uri.String())
	if r.panics {
		panic("host connection lost")
	}
	return r.showErr
}

func (r *linkClickIO) Notify(
	_ browserapi.NotificationLevel, format string, args ...any,
) (string, error) {
	r.record(fmt.Sprintf(format, args...))
	return "", nil
}

func (r *linkClickIO) record(call string) {
	if r.lock.held.Load() {
		call += " (locked)"
	}
	r.calls = append(r.calls, call)
}

func TestDialogueLinkClickLock(t *testing.T) {
	const (
		width  = 40
		height = 10
		link   = "https://example.com/rune"
	)
	tests := []struct {
		name      string
		cwd       string
		link      string
		showErr   error
		panics    bool
		wantCalls []string
	}{
		{
			name:      "show runs unlocked",
			link:      link,
			wantCalls: []string{"show " + link},
		},
		{
			name:      "relative runbook link from assistant response",
			cwd:       "file:///Users/ernestrc/src/ox-api",
			link:      "deploy/reports/README.md",
			wantCalls: []string{"show file:///Users/ernestrc/src/ox-api/deploy/reports/README.md"},
		},
		{
			name:    "failed relative link notifies unlocked",
			cwd:     "file:///workspace/project",
			link:    "missing.md",
			showErr: errors.New("host refused"),
			wantCalls: []string{
				"show file:///workspace/project/missing.md",
				"open missing.md: host refused",
			},
		},
		{
			name:      "remote runbook link",
			cwd:       "ssh://alice@example.com:2222/work/project",
			link:      "deploy/reports/README.md",
			wantCalls: []string{"show ssh://alice@example.com:2222/work/project/deploy/reports/README.md"},
		},
		{
			name:      "failed show notifies unlocked",
			link:      link,
			showErr:   errors.New("host refused"),
			wantCalls: []string{"show " + link, "open " + link + ": host refused"},
		},
		{
			name:      "panicking show hands the lock back",
			link:      link,
			panics:    true,
			wantCalls: []string{"show " + link},
		},
		{
			name: "declined link does no I/O",
			link: "#section",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lock := &dialogueLock{}
			io := &linkClickIO{
				lock:    lock,
				showErr: tt.showErr,
				panics:  tt.panics,
			}
			h := &aiEditorHandler{o: io, n: io}
			if tt.cwd != "" {
				var err error
				h.cwd, err = workspaceapi.ParseURI(tt.cwd)
				require.NoError(t, err)
			}
			comp := h.newDialogueComponent(lock)
			dh, tx, _ := dialoguetui.Handler(context.Background(), lock, comp, term.NopInterrupter())
			defer close(tx)
			dh.Resize(width, height)
			comp.AddReceiveMessage("See [the rollout runbook](" + tt.link + ").")
			w := term.NewStringWriter(width, height)
			dh.Draw(w)
			pos, ok := cellOf(w, width, "the rollout runbook")
			require.True(t, ok, "link drawn")

			click := func() {
				dh.Handle(term.Event{
					Type: term.EventMouse, Key: term.MouseLeft, MouseX: pos.X, MouseY: pos.Y,
				})
			}
			if tt.panics {
				assert.Panics(t, click)
			} else {
				click()
			}

			assert.Equal(t, tt.wantCalls, io.calls)
			assert.Zero(t, lock.badUnlocks.Load(), "unlocked while not held")
			assert.False(t, lock.held.Load(), "dialogue still holds its lock")
		})
	}
}

// cellOf returns the screen cell where text starts in w.
func cellOf(w *term.StringWriter, width int, text string) (term.Coordinates, bool) {
	cells, want := w.Cells(), []rune(text)
	for i := range cells {
		n := 0
		for n < len(want) && i+n < len(cells) && cells[i+n].Ch == want[n] {
			n++
		}
		if n == len(want) {
			return term.Coordinates{X: i % width, Y: i / width}, true
		}
	}
	return term.Coordinates{}, false
}
