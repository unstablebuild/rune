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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

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
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/component/asciiart"
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
			comp := dialoguetui.NewComponent(dialoguetui.DefaultComponentConfig())
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
			cfg := dialoguetui.DefaultComponentConfig()
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
				cfg: dialoguetui.DefaultComponentConfig(), imageCache: tt.cache, p: term.NopInterrupter(),
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

// The system prompt markers by which skillChat tells its agents apart.
const (
	chatAgent     = "CHAT-AGENT"
	defaultAgent  = "DEFAULT-AGENT"
	researchAgent = "RESEARCH-AGENT"
	scoutAgent    = "SCOUT-AGENT"
	recapAgent    = "RECAP-AGENT"
	defaultSkill  = "DEFAULT-SKILL"
)

// skillChat is an agent chat opened as the chat command opens one, over a
// workspace holding notes.txt and skills for every way to run a skill or
// a sub-agent:
//
//   - notes and other are instruction skills.
//   - research is an agent skill whose tool rules allow reading files,
//     the notes skill, scout sub-agents, git log but not git log -p, and
//     its own scripts.
//   - scout is an agent skill with no tool rules.
//   - recap is an agent skill that starts with the conversation so far.
//   - default is an agent skill named like the default agent.
//
// Only the model and the shell are fakes: shell commands are recorded,
// not run, and sub-agent conversations are numbered in the order they
// start, as in sub-agent-research-1.
type skillChat struct {
	t        *testing.T
	h        *aiEditorHandler
	wm       *recordingWindowManager
	tab      browserapi.Handler
	llm      *agentRouter
	exec     *recordingExec
	store    *memDialogueStore
	skillDir string
}

const (
	skillChatID                     = "skills-chat"
	skillChatWidth, skillChatHeight = 100, 50
)

// newSkillDir returns a directory holding the skills of a skillChat.
func newSkillDir(t *testing.T) string {
	skillDir := t.TempDir()
	for name, content := range map[string]string{
		"notes/SKILL.md": "---\nname: notes\ndescription: How to take notes\n---\nNOTES-SKILL: Take notes as bullet points.",
		"other/SKILL.md": "---\nname: other\ndescription: Something else\n---\nOTHER-SKILL: Do something else.",
		"research/SKILL.md": `---
name: research
description: Researches the notes
type: agent
allowed-tools: [read_file, "skill(notes)", "agent(scout)", "bash(git log *)", "bash($research)"]
disallowed-tools: ["bash(git log -p *)"]
---
` + researchAgent + `: You research the notes.`,
		"research/scripts/summarize": "#!/bin/sh\n",
		"scout/SKILL.md": "---\nname: scout\ndescription: Scouts the workspace\ntype: agent\n---\n" +
			scoutAgent + ": You scout the workspace.",
		"recap/SKILL.md": "---\nname: recap\ndescription: Recaps the conversation\ntype: agent\n" +
			"parent-context: true\nallowed-tools: [read_file]\n---\n" + recapAgent + ": You recap the conversation.",
		"default/SKILL.md": "---\nname: default\ndescription: Named like the default agent\ntype: agent\n" +
			"allowed-tools: [read_file]\n---\n" + defaultSkill + ": You only read files.",
	} {
		file := filepath.Join(skillDir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(content), 0o755))
	}
	return skillDir
}

// newSkillChat opens a skillChat over the skills in skillDir, which
// newSkillDir writes.
func newSkillChat(t *testing.T, skillDir string) *skillChat {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hi\n"), 0o644))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fs, cwd := testLocalFS{root: workspace}, dirURI(workspace)
	model := llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000}
	c := &skillChat{
		t:  t,
		wm: &recordingWindowManager{},
		llm: newAgentRouter([]llmapi.ModelEntry{model},
			chatAgent, defaultAgent, researchAgent, scoutAgent, recapAgent, defaultSkill),
		exec:     &recordingExec{},
		store:    newMemDialogueStore(),
		skillDir: skillDir,
	}
	tools, _ := agentools.DefaultTools(fs, c.exec, cwd, stubLSP{}, agentools.NoIndex(), agentools.Config{},
		configedit.NopConfig())
	var started atomic.Int32
	c.h = &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         c.llm,
		defaultModel:   model.Name,
		cfg:            dialoguetui.DefaultComponentConfig(),
		dialogueStore:  c.store,
		systemPrompt:   chatAgent + ": You are the chat agent.",
		agentsConfig:   defaultAgentsConfig(defaultAgent + ": You are a sub-agent."),
		baseTools:      tools,
		toolRegistry:   agent.NewRegistry(tools...),
		skillRegistry:  skills.NewRegistry(fs, cwd, []string{skillDir}, nil),
		wm:             c.wm,
		n:              stubNotifications{},
		p:              term.NopInterrupter(),
		config:         configedit.NopConfig(),
		cwd:            cwd,
		fs:             fs,
		memoryDataPath: t.TempDir(),
		generateDialogueID: func(_ context.Context, agentID string) string {
			return fmt.Sprintf("sub-agent-%s-%d", agentID, started.Add(1))
		},
	}
	c.open()
	t.Cleanup(func() { require.NoError(t, c.tab.Close()) })
	return c
}

// open opens the chat in a tab, replaying what it saved.
func (c *skillChat) open() {
	require.NoError(c.t, c.h.handleChat(textapi.Command{Args: []string{skillChatID}}))
	c.tab = c.wm.gotHandler
	c.tab.Resize(skillChatWidth, skillChatHeight)
}

// reopen closes the chat's tab and opens the chat again.
func (c *skillChat) reopen() {
	require.NoError(c.t, c.tab.Close())
	c.open()
}

// send types text into the chat and submits it.
func (c *skillChat) send(text string) {
	keys, err := term.ParseKeys(strings.ReplaceAll(text, " ", "<space>") + "<enter>")
	require.NoError(c.t, err)
	for _, key := range keys {
		c.tab.Handle(term.Event{Type: term.EventKey, Ch: key.Ch, Key: key.Key, Mod: key.Mod})
	}
}

// chatskill runs the chatskill command on the chat, as the command
// prompt does with the words of line.
func (c *skillChat) chatskill(line string) {
	require.NoError(c.t, c.h.HandleCommand(c.h.ctx, textapi.Command{
		Name: commandSkill, Args: strings.Fields(line), URI: c.wm.gotURI,
	}))
}

func (c *skillChat) screen() string {
	return handlertest.DrawHandler(c.tab, skillChatWidth, skillChatHeight)
}

// saved returns the messages the conversation id holds.
func (c *skillChat) saved(id string) []llmapi.Message {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	return slices.Clone(c.store.dialogues[id].Messages)
}

// subAgents returns the IDs of the sub-agent conversations, sorted.
func (c *skillChat) subAgents() []string {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	var ids []string
	for id := range c.store.dialogues {
		if strings.HasPrefix(id, "sub-agent-") {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// await waits until the chat shows reply and holds want after its system
// prompt, and returns the screen.
func (c *skillChat) await(reply string, want []llmapi.Message) string {
	var screen string
	require.EventuallyWithT(c.t, func(t *assert.CollectT) {
		screen = c.screen()
		if assert.Contains(t, screen, reply) {
			saved := c.saved(skillChatID)
			if assert.NotEmpty(t, saved) {
				assert.Equal(t, want, saved[1:], "screen:\n%s", screen)
			}
		}
	}, 5*time.Second, 10*time.Millisecond)
	return screen
}

// scriptResearch scripts the research sub-agent to try every kind of tool
// its rules allow or forbid, including a scout sub-agent that tries a
// command they forbid, and to then answer "The notes say hi.".
func (c *skillChat) scriptResearch() {
	script := filepath.Join(c.skillDir, "research", "scripts", "summarize")
	c.llm.script(researchAgent,
		toolCallsResponse(
			toolCall("r-read", "read_file", `{"path":"notes.txt"}`),
			toolCall("r-log", "bash", `{"command":"git log --oneline","description":"log"}`),
			toolCall("r-log-p", "bash", `{"command":"git log -p notes.txt","description":"log"}`),
			toolCall("r-rm", "bash", `{"command":"rm notes.txt","description":"remove"}`),
			toolCall("r-script", "bash", mustJSON(c.t, map[string]string{
				"command": script + " notes.txt", "description": "summarize",
			})),
			toolCall("r-notes", "skill", `{"name":"notes","args":null}`),
			toolCall("r-other", "skill", `{"name":"other","args":null}`),
			toolCall("r-scout", "agent", `{"description":"scout","prompt":"look around","subagent_type":"scout"}`),
			toolCall("r-default", "agent", `{"description":"anything","prompt":"do anything"}`),
			toolCall("r-patch", "apply_patch", `{"patch":""}`),
		),
		llmtest.Response{Chunks: []string{"The notes say hi."}},
	)
	c.llm.script(scoutAgent,
		toolCallsResponse(toolCall("s-rm", "bash", `{"command":"rm notes.txt","description":"remove"}`)),
		llmtest.Response{Chunks: []string{"Scout found notes.txt."}},
	)
}

// checkResearch checks that the research sub-agent ran as scriptResearch
// scripts it, given task, and that its tool rules bound it and the scout
// sub-agent it ran.
func (c *skillChat) checkResearch(task string) {
	t := c.t
	research := c.llm.requests(researchAgent)
	require.Len(t, research, 2)
	first := research[0].Request
	assert.Equal(t, researchAgent+": You research the notes.", first.Messages[0].Content)
	assert.Equal(t, []llmapi.Message{{Role: llmapi.RoleUser, Content: task}}, conversation(first.Messages),
		"the sub-agent starts with the task alone")
	assert.Equal(t, []string{"agent", "bash", "read_file", "skill"}, toolNames(first))
	assert.Contains(t, toolDescription(first, "bash"), "Commands are restricted")
	assert.Contains(t, toolDescription(first, "agent"), "- scout: Scouts the workspace")
	assert.NotContains(t, toolDescription(first, "agent"), "- research")
	skillList := systemMessage(first, "The following skills are available")
	assert.Contains(t, skillList, "- notes: How to take notes")
	for _, hidden := range []string{"other", "research", "scout", "explore"} {
		assert.NotContains(t, skillList, "- "+hidden, "the rules hide the skills they do not allow")
	}

	results := research[1].Request.Messages
	script := filepath.Join(c.skillDir, "research", "scripts", "summarize")
	for id, want := range map[string]string{
		"r-read":    "L1: hi",
		"r-log":     "ran bash -c git log --oneline",
		"r-log-p":   "error: ",
		"r-rm":      "error: ",
		"r-script":  "ran bash -c " + script + " notes.txt",
		"r-notes":   "NOTES-SKILL: Take notes as bullet points.",
		"r-other":   `unknown skill "other"`,
		"r-scout":   "Scout found notes.txt.",
		"r-default": "error: subagent_type is required",
		"r-patch":   `error: unknown tool "apply_patch"`,
	} {
		got, ok := findToolResult(results, id)
		if assert.True(t, ok, "no result for %s", id) {
			assert.Contains(t, got, want, "result of %s", id)
		}
	}

	scout := c.llm.requests(scoutAgent)
	require.Len(t, scout, 2)
	assert.Equal(t, toolNames(first), toolNames(scout[0].Request),
		"a sub-agent gets no more than the agent that runs it")
	got, _ := findToolResult(scout[1].Request.Messages, "s-rm")
	assert.True(t, strings.HasPrefix(got, "error: "), "the scout's rm must be rejected: %s", got)
	assert.Equal(t, []string{"bash -c git log --oneline", "bash -c " + script + " notes.txt"}, c.exec.ran(),
		"only allowed commands may run")
}

func TestE2ESkillRuns(t *testing.T) {
	// researchRows are the rows of the calls of research, as scriptResearch
	// scripts it, and of its result.
	researchRows := []string{
		"├─ ✓ read_file notes.txt",
		"├─ ✓ bash git log --oneline",
		"├─ ✗ bash git log -p notes.txt",
		"├─ ✗ bash rm notes.txt",
		"├─ ✓ bash /", // the script, by its absolute path
		"├─ ✓ skill notes",
		"├─ ✗ skill other",
		"├─ ✓ agent scout",
		"│  ├─ ✗ bash rm notes.txt",
		"│  └─ 󰆈  Scout found notes.txt.",
		"├─ ✗ agent anything",
		"├─ ✗ apply_patch",
		"└─ 󰆈  The notes say hi.",
	}
	skillDir := newSkillDir(t)
	notes, ok := skills.NewRegistry(testLocalFS{root: "/"}, dirURI(""), []string{skillDir}, nil).Get("notes")
	require.True(t, ok)
	tests := []struct {
		name string
		// greet has the chat agent answer a greeting before the run.
		greet bool
		// run starts the run as the user does, who sees shown.
		run   func(c *skillChat)
		shown string
		// unechoed reports that the chat only shows the user's message
		// once reopened.
		unechoed bool
		// research is the task the research sub-agent gets, when the run
		// has it run as scriptResearch scripts it.
		research string
		scripts  map[string][]llmtest.Response
		// reply is the last text the run shows, turn what the chat saves
		// of it, and rows the row of the run's tool call followed by
		// the rows nested under it.
		reply     string
		turn      []llmapi.Message
		rows      []string
		subAgents []string
		check     func(t *testing.T, c *skillChat)
	}{
		{
			name:      "slash command runs an agent skill",
			run:       func(c *skillChat) { c.send("/research what do the notes say?") },
			shown:     "/research what do the notes say?",
			research:  skillCommand("research", "what do the notes say?"),
			reply:     "The notes say hi.",
			turn:      ranSkill(skillCommand("research", "what do the notes say?"), "research", 1, "The notes say hi."),
			rows:      append([]string{"✓ skill research"}, researchRows...),
			subAgents: []string{"sub-agent-research-1", "sub-agent-scout-2"},
			check: func(t *testing.T, c *skillChat) {
				assert.Empty(t, c.llm.requests(chatAgent), "the sub-agent answers, not the chat agent")
			},
		},
		{
			name:      "chatskill runs an agent skill",
			run:       func(c *skillChat) { c.chatskill("research what do the notes say?") },
			shown:     "/research what do the notes say?",
			unechoed:  true,
			research:  skillCommand("research", "what do the notes say?"),
			reply:     "The notes say hi.",
			turn:      ranSkill(skillCommand("research", "what do the notes say?"), "research", 1, "The notes say hi."),
			rows:      append([]string{"✓ skill research"}, researchRows...),
			subAgents: []string{"sub-agent-research-1", "sub-agent-scout-2"},
			check: func(t *testing.T, c *skillChat) {
				assert.Empty(t, c.llm.requests(chatAgent), "the sub-agent answers, not the chat agent")
			},
		},
		{
			name:     "skill tool runs an agent skill",
			run:      func(c *skillChat) { c.send("research the notes") },
			shown:    "research the notes",
			research: "what do the notes say?",
			scripts: map[string][]llmtest.Response{chatAgent: {
				toolCallsResponse(toolCall("k1", "skill", `{"name":"research","args":"what do the notes say?"}`)),
				{Chunks: []string{"Research says the notes say hi."}},
			}},
			reply: "Research says the notes say hi.",
			turn: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "research the notes"},
				{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{
					toolCall("k1", "skill", `{"name":"research","args":"what do the notes say?"}`),
				}},
				{Role: llmapi.RoleTool, Content: "The notes say hi.", Name: "skill", ToolCallID: "k1"},
				{Role: llmapi.RoleAssistant, Content: "Research says the notes say hi."},
			},
			rows:      append([]string{"✓ skill research what do the notes say?"}, researchRows...),
			subAgents: []string{"sub-agent-research-1", "sub-agent-scout-2"},
		},
		{
			name:     "agent tool runs an agent skill",
			run:      func(c *skillChat) { c.send("research the notes") },
			shown:    "research the notes",
			research: "what do the notes say?",
			scripts: map[string][]llmtest.Response{chatAgent: {
				toolCallsResponse(toolCall("a1", "agent",
					`{"description":"research the notes","prompt":"what do the notes say?","subagent_type":"research"}`)),
				{Chunks: []string{"Research says the notes say hi."}},
			}},
			reply: "Research says the notes say hi.",
			turn: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "research the notes"},
				{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{toolCall("a1", "agent",
					`{"description":"research the notes","prompt":"what do the notes say?","subagent_type":"research"}`)}},
				{Role: llmapi.RoleTool, Content: "The notes say hi.", Name: "agent", ToolCallID: "a1"},
				{Role: llmapi.RoleAssistant, Content: "Research says the notes say hi."},
			},
			rows:      append([]string{"✓ agent research the notes"}, researchRows...),
			subAgents: []string{"sub-agent-research-1", "sub-agent-scout-2"},
		},
		{
			name:  "agent tool runs the default agent",
			run:   func(c *skillChat) { c.send("look around") },
			shown: "look around",
			scripts: map[string][]llmtest.Response{
				chatAgent: {
					toolCallsResponse(toolCall("a1", "agent", `{"description":"look around","prompt":"look around"}`)),
					{Chunks: []string{"The sub-agent looked around."}},
				},
				defaultAgent: {{Chunks: []string{"Looked around."}}},
			},
			reply: "The sub-agent looked around.",
			turn: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "look around"},
				{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{
					toolCall("a1", "agent", `{"description":"look around","prompt":"look around"}`),
				}},
				{Role: llmapi.RoleTool, Content: "Looked around.", Name: "agent", ToolCallID: "a1"},
				{Role: llmapi.RoleAssistant, Content: "The sub-agent looked around."},
			},
			rows:      []string{"✓ agent look around", "└─ 󰆈  Looked around."},
			subAgents: []string{"sub-agent-default-1"},
			check: func(t *testing.T, c *skillChat) {
				requests := c.llm.requests(defaultAgent)
				require.Len(t, requests, 1)
				tools := toolNames(requests[0].Request)
				for _, tool := range []string{"agent", "apply_patch", "bash", "read_file", "skill"} {
					assert.Contains(t, tools, tool, "an agent without tool rules gets every tool")
				}
				skillList := systemMessage(requests[0].Request, "The following skills are available")
				assert.Contains(t, skillList, "- other: Something else")
				assert.Contains(t, skillList, "- research (agent): Researches the notes")
			},
		},
		{
			name:  "slash command loads an instruction skill",
			run:   func(c *skillChat) { c.send("/notes about today") },
			shown: "/notes about today",
			scripts: map[string][]llmtest.Response{chatAgent: {
				toolCallsResponse(toolCall("n1", "skill", `{"name":"notes","args":"about today"}`)),
				{Chunks: []string{"Noted."}},
			}},
			reply: "Noted.",
			turn: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: skillCommand("notes", "about today") +
					"\n<command-hint>Call the skill tool with name=\"notes\" to load and run this skill.</command-hint>"},
				{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{
					toolCall("n1", "skill", `{"name":"notes","args":"about today"}`),
				}},
				{Role: llmapi.RoleTool, Content: skills.FormatSkillContent(notes), Name: "skill", ToolCallID: "n1"},
				{Role: llmapi.RoleAssistant, Content: "Noted."},
			},
			rows: []string{"✓ skill notes about today"},
			check: func(t *testing.T, c *skillChat) {
				requests := c.llm.requests(chatAgent)
				require.NotEmpty(t, requests)
				assert.Equal(t, skills.FormatSkillContent(notes),
					systemMessage(requests[0].Request, "NOTES-SKILL"), "the command loads the skill")
			},
		},
		{
			name:      "slash command runs an agent skill with the conversation",
			greet:     true,
			run:       func(c *skillChat) { c.send("/recap") },
			shown:     "/recap",
			scripts:   map[string][]llmtest.Response{recapAgent: {{Chunks: []string{"You said hello."}}}},
			reply:     "You said hello.",
			turn:      ranSkill(skillCommand("recap", ""), "recap", 1, "You said hello."),
			rows:      []string{"✓ skill recap", "└─ 󰆈  You said hello."},
			subAgents: []string{"sub-agent-recap-1"},
			check: func(t *testing.T, c *skillChat) {
				requests := c.llm.requests(recapAgent)
				require.Len(t, requests, 1)
				assert.Equal(t, append(greeting(), llmapi.Message{Role: llmapi.RoleUser, Content: skillCommand("recap", "")}),
					conversation(requests[0].Request.Messages))
			},
		},
		{
			name:  "slash command runs a skill named like an agent",
			greet: true,
			run:   func(c *skillChat) { c.send("/default read the notes") },
			shown: "/default read the notes",
			scripts: map[string][]llmtest.Response{defaultSkill: {
				toolCallsResponse(toolCall("d-ls", "bash", `{"command":"ls","description":"list"}`)),
				{Chunks: []string{"Read the notes."}},
			}},
			reply:     "Read the notes.",
			turn:      ranSkill(skillCommand("default", "read the notes"), "default", 1, "Read the notes."),
			rows:      []string{"✓ skill default", "├─ ✗ bash command=ls description=list", "└─ 󰆈  Read the notes."},
			subAgents: []string{"sub-agent-default-1"},
			check: func(t *testing.T, c *skillChat) {
				assert.Empty(t, c.llm.requests(defaultAgent), "the skill must not run as the agent")
				requests := c.llm.requests(defaultSkill)
				require.Len(t, requests, 2)
				assert.Equal(t, []string{"read_file"}, toolNames(requests[0].Request))
				assert.Equal(t, []llmapi.Message{{Role: llmapi.RoleUser, Content: skillCommand("default", "read the notes")}},
					conversation(requests[0].Request.Messages), "the sub-agent starts with the task alone")
				got, _ := findToolResult(requests[1].Request.Messages, "d-ls")
				assert.Equal(t, `error: unknown tool "bash"`, got)
				assert.Empty(t, c.exec.ran())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newSkillChat(t, skillDir)
			var before []llmapi.Message
			if tt.greet {
				c.llm.script(chatAgent, llmtest.Response{Chunks: []string{"Hi there."}})
				c.send("hello")
				before = greeting()
				c.await("Hi there.", before)
			}
			if tt.research != "" {
				c.scriptResearch()
			}
			for marker, responses := range tt.scripts {
				c.llm.script(marker, responses...)
			}

			tt.run(c)
			turn := append(slices.Clone(before), tt.turn...)
			screen := c.await(tt.reply, turn)
			if !tt.unechoed {
				assert.Contains(t, screen, tt.shown)
			}
			rows := runRows(screen)
			if assert.Len(t, rows, len(tt.rows), "screen:\n%s", screen) {
				for i, want := range tt.rows {
					assert.True(t, strings.HasPrefix(rows[i], want), "row %d is %q, want %q", i, rows[i], want)
				}
			}
			assert.Greater(t, strings.LastIndex(screen, tt.reply), strings.Index(screen, tt.rows[0]),
				"the reply follows the run:\n%s", screen)
			if tt.research != "" {
				c.checkResearch(tt.research)
			}
			assert.Equal(t, tt.subAgents, c.subAgents())
			if tt.check != nil {
				tt.check(t, c)
			}

			c.reopen()
			reopened := c.screen()
			assert.Contains(t, reopened, tt.shown, "the reopened chat shows the run")
			assert.Contains(t, reopened, tt.reply, "the reopened chat shows the run")
			// Reopened, a call shows its arguments instead of its summary.
			if rows := runRows(reopened); assert.NotEmpty(t, rows, "screen:\n%s", reopened) {
				tool := strings.Join(strings.Fields(tt.rows[0])[:2], " ")
				assert.True(t, strings.HasPrefix(rows[0], tool+" "), "row %q, want %s", rows[0], tool)
			}
			asked := len(c.llm.requests(chatAgent))
			c.llm.script(chatAgent, llmtest.Response{Chunks: []string{"You're welcome."}})
			c.send("thanks")
			c.await("You're welcome.", append(slices.Clone(turn),
				llmapi.Message{Role: llmapi.RoleUser, Content: "thanks"},
				llmapi.Message{Role: llmapi.RoleAssistant, Content: "You're welcome."}))
			requests := c.llm.requests(chatAgent)
			require.Len(t, requests, asked+1)
			assert.Equal(t, append(turn, llmapi.Message{Role: llmapi.RoleUser, Content: "thanks"}),
				conversation(requests[asked].Request.Messages), "the chat agent answers knowing the run")
		})
	}
}

// skillCommand returns the message the chat sends for /name args.
func skillCommand(name, args string) string {
	msg := "<command-message>" + name + "</command-message>\n<command-name>/" + name + "</command-name>"
	if args != "" {
		msg += "\n" + args
	}
	return msg
}

// ranSkill returns what the chat saves of the user's message input running
// the agent skill name in its n-th sub-agent, which replied reply.
func ranSkill(input, name string, n int, reply string) []llmapi.Message {
	call := toolCall(fmt.Sprintf("skill-sub-agent-%s-%d", name, n), "skill", `{"name":"`+name+`"}`)
	return []llmapi.Message{
		{Role: llmapi.RoleUser, Content: input},
		{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{call}},
		{Role: llmapi.RoleTool, Content: reply, Name: "skill", ToolCallID: call.ID},
		{Role: llmapi.RoleAssistant, Content: reply},
	}
}

// greeting returns what the chat saves of a greeting the chat agent
// answered.
func greeting() []llmapi.Message {
	return []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
		{Role: llmapi.RoleAssistant, Content: "Hi there."},
	}
}

// toolDurationRegexp matches the duration a finished tool call shows
// after its name.
var toolDurationRegexp = regexp.MustCompile(`([✓✗] \S+) [0-9.]+(ns|µs|ms|s|m)\b`)

// runRows returns the first top-level tool call row of screen and the rows
// nested under it, without durations and with the nested rows relative
// to it.
func runRows(screen string) []string {
	var rows []string
	margin := 0
	for line := range strings.SplitSeq(screen, "\n") {
		line = toolDurationRegexp.ReplaceAllString(strings.TrimRight(line, " "), "$1")
		if rows == nil {
			trimmed := strings.TrimLeft(line, " ")
			for _, glyph := range []string{"├─ ", "└─ "} {
				if root, ok := strings.CutPrefix(trimmed, glyph); ok {
					rows, margin = append(rows, root), len(line)-len(trimmed)
				}
			}
			continue
		}
		if len(line) < margin {
			break
		}
		nested, ok := strings.CutPrefix(line[margin:], "│  ")
		if !ok {
			nested, ok = strings.CutPrefix(line[margin:], "   ")
		}
		if !ok || strings.HasPrefix(nested, " ") {
			break
		}
		rows = append(rows, nested)
	}
	return rows
}

func toolCallsResponse(calls ...llmapi.ToolCall) llmtest.Response {
	return llmtest.Response{ToolCalls: calls, FinishReason: llmapi.FinishReasonToolCall}
}

// conversation returns msgs without their system messages.
func conversation(msgs []llmapi.Message) []llmapi.Message {
	var ret []llmapi.Message
	for _, m := range msgs {
		if m.Role != llmapi.RoleSystem {
			ret = append(ret, m)
		}
	}
	return ret
}

func toolNames(req llmapi.Request) []string {
	var names []string
	for _, tool := range req.Tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

func toolDescription(req llmapi.Request, name string) string {
	for _, tool := range req.Tools {
		if tool.Function.Name == name {
			return tool.Function.Description
		}
	}
	return ""
}

// systemMessage returns the first system message of req that holds text.
func systemMessage(req llmapi.Request, text string) string {
	for _, m := range req.Messages {
		if m.Role == llmapi.RoleSystem && strings.Contains(m.Content, text) {
			return m.Content
		}
	}
	return ""
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

func TestHandlerReplaceMCPToolsIsConcurrentWithReaders(t *testing.T) {
	t.Parallel()

	h := &aiEditorHandler{
		baseTools:    []agent.Tool{stubTool{}},
		toolRegistry: agent.NewRegistry(),
	}
	snapshot := h.tools()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.replaceMCPTools("srv", []agent.Tool{stubTool{name: fmt.Sprint(i)}}, nil)
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
	h.replaceMCPTools("srv", []agent.Tool{stubTool{name: "early"}}, nil)

	snapshot := h.tools()
	chatReg := agent.NewRegistry(snapshot...)

	h.replaceMCPTools("srv", []agent.Tool{stubTool{name: "mid"}}, nil)
	h.subscribeTools("chat-1", snapshot, chatReg)
	_, ok := chatReg.Get("mid", "")
	assert.True(t, ok, "delta between snapshot and subscription must be applied")

	h.replaceMCPTools("srv", []agent.Tool{stubTool{name: "late"}}, nil)
	_, ok = chatReg.Get("late", "")
	assert.True(t, ok, "subscribed chats must receive late tools")
	_, ok = h.toolRegistry.Get("late", "")
	assert.True(t, ok, "the persistent registry must receive late tools")

	h.unsubscribeTools("chat-1")
	h.replaceMCPTools("srv", []agent.Tool{stubTool{name: "after-close"}}, nil)
	_, ok = chatReg.Get("after-close", "")
	assert.False(t, ok, "closed chats must not be updated")
}

func TestSubscribedChatFollowsMCPToolRemovals(t *testing.T) {
	t.Parallel()

	builtin := stubTool{name: "read_file"}
	h := &aiEditorHandler{
		baseTools:    []agent.Tool{builtin},
		toolRegistry: agent.NewRegistry(builtin),
	}
	h.replaceMCPTools("srv", []agent.Tool{
		stubTool{name: "mcp__srv__a"}, stubTool{name: "mcp__srv__b"},
	}, nil)

	snapshot := h.tools()
	chatReg := agent.NewRegistry(snapshot...)
	// Between the snapshot and the subscription a tool goes away and
	// another one changes its definition.
	h.replaceMCPTools("srv", []agent.Tool{
		describedTool{stubTool{name: "mcp__srv__b"}, "v2"},
	}, []string{"mcp__srv__a", "mcp__srv__b"})
	h.subscribeTools("chat-1", snapshot, chatReg)

	assert.Equal(t, []string{"mcp__srv__b", "read_file"}, registryToolNames(chatReg))
	got, ok := chatReg.Get("mcp__srv__b", "")
	require.True(t, ok)
	assert.Equal(t, "v2", got.Definition().Function.Description,
		"a redefined tool must replace the snapshot's copy")

	h.replaceMCPTools("srv", nil, []string{"mcp__srv__b"})
	assert.Equal(t, []string{"read_file"}, registryToolNames(chatReg),
		"subscribed chats must lose removed tools")
	assert.Equal(t, []string{"read_file"}, registryToolNames(h.toolRegistry))
	assert.Len(t, h.tools(), 1)
}

// describedTool is a stubTool with a description, standing in for a
// tool whose server changed its definition.
type describedTool struct {
	stubTool
	description string
}

func (t describedTool) Definition() llmapi.Tool {
	def := t.stubTool.Definition()
	def.Function.Description = t.description
	return def
}

func registryToolNames(r *agent.Registry) []string {
	var names []string
	for _, t := range r.AllTools() {
		names = append(names, t.Function.Name)
	}
	return names
}

func TestMCPChatWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		srv  runemcp.ServerInfo
		want string
	}{
		{
			name: "starting",
			srv:  runemcp.ServerInfo{Name: "fs", Status: runemcp.StatusConnecting},
			want: `MCP server "fs" is still starting. Its tools will join this conversation once connected.`,
		},
		{
			name: "needs sign-in",
			srv: runemcp.ServerInfo{Name: "linear", Status: runemcp.StatusNeedsAuth,
				Error: "needs sign-in: run `agent mcp login linear`"},
			want: "MCP server \"linear\" needs sign-in: run `agent mcp login linear`. " +
				"Its tools will join this conversation once you sign in.",
		},
		{
			name: "needs a wider sign-in",
			srv: runemcp.ServerInfo{Name: "linear", Status: runemcp.StatusNeedsAuth,
				Error: "needs sign-in: run `agent mcp login linear` (the server requires scope \"write\")"},
			want: "MCP server \"linear\" needs sign-in: run `agent mcp login linear` " +
				"(the server requires scope \"write\"). Its tools will join this conversation once you sign in.",
		},
		{
			name: "signed out",
			srv:  runemcp.ServerInfo{Name: "linear", Status: runemcp.StatusNeedsAuth},
			want: "MCP server \"linear\" needs sign-in: run `agent mcp login linear`. " +
				"Its tools will join this conversation once you sign in.",
		},
		{
			name: "failed",
			srv:  runemcp.ServerInfo{Name: "fs", Status: runemcp.StatusError, Error: "exit status 1"},
			want: "MCP server \"fs\" is unavailable: exit status 1\nRun `agent mcp reconnect fs` to try again.",
		},
		{
			name: "not trusted",
			srv: runemcp.ServerInfo{Name: "docs", Status: runemcp.StatusError, Blocked: true,
				Error: "not trusted: run `agent mcp trust docs`"},
			want: "MCP server \"docs\" is unavailable: not trusted: run `agent mcp trust docs`",
		},
		{name: "connected", srv: runemcp.ServerInfo{Name: "fs", Status: runemcp.StatusConnected}},
		{name: "shut down", srv: runemcp.ServerInfo{Name: "fs", Status: runemcp.StatusDisconnected}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, mcpChatWarning(&tt.srv))
		})
	}
}

// newMCPHandlerInEveryState returns a handler with one MCP server in
// each state a chat is warned about, and one connected server. The
// servers start before the handler so that, cleaning up in reverse, the
// handler's sessions end before the servers stop.
func newMCPHandlerInEveryState(t *testing.T) (*aiEditorHandler, *capturingNotifications) {
	t.Helper()
	mux, guard := signInMux(t)
	signIn := serveMCP(t, mux, guard)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no MCP here", http.StatusBadRequest)
	}))
	t.Cleanup(broken.Close)
	up := serveMCP(t, http.NewServeMux(), nil)
	h, noti, _ := newMCPTestHandler(t, "file:///work/repo", nil)
	ctx := context.Background()

	h.mcpManager.Register("pending", runemcp.ServerConfig{URL: "https://pending.example.com/mcp"}, nil, nil)
	h.mcpManager.Register("untrusted", runemcp.ServerConfig{URL: "https://mcp.example.com/mcp"}, nil,
		errors.New("not trusted: run `agent mcp trust untrusted`"))
	_, err := h.mcpManager.ConnectServer(ctx, "linear", runemcp.ServerConfig{URL: signIn + "/mcp"})
	require.ErrorIs(t, err, runemcp.ErrNeedsAuth)
	_, err = h.mcpManager.ConnectServer(ctx, "broken", runemcp.ServerConfig{URL: broken.URL + "/mcp"})
	require.Error(t, err)
	_, err = h.mcpManager.ConnectServer(ctx, "up", runemcp.ServerConfig{URL: up + "/mcp"})
	require.NoError(t, err)
	return h, noti
}

// openTestChat registers an open chat with h, as opening a chat tab does.
func openTestChat(h *aiEditorHandler, id string) syncComponent {
	sc := syncComponent{mu: new(sync.Mutex), comp: dialoguetui.NewComponent(dialoguetui.ComponentConfig{}), h: h}
	h.openChats.Store(id, sc)
	return sc
}

// chatText returns the words a chat shows.
func chatText(t *testing.T, sc syncComponent) string {
	t.Helper()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	const width, height = 200, 60
	sc.comp.Resize(width, height)
	w := term.NewStringWriter(width, height)
	sc.comp.Draw(w)
	require.NoError(t, w.Flush())
	return strings.Join(strings.Fields(w.String()), " ")
}

func TestWarnUnavailableMCPServers(t *testing.T) {
	t.Parallel()

	h, _ := newMCPHandlerInEveryState(t)
	tx := make(chan dialoguetui.MessageEvent, 1)

	h.warnUnavailableMCPServers(tx)

	require.Len(t, tx, 1, "every unavailable server is explained in one warning")
	ev := <-tx
	assert.Equal(t, dialoguetui.MessageEventWarning, ev.Type)
	assert.Contains(t, ev.Text, `MCP server "pending" is still starting.`)
	assert.Contains(t, ev.Text, "MCP server \"linear\" needs sign-in: run `agent mcp login linear`.")
	assert.Contains(t, ev.Text, "MCP server \"untrusted\" is unavailable: not trusted: run `agent mcp trust untrusted`")
	assert.Contains(t, ev.Text, "Run `agent mcp reconnect broken` to try again.")
	assert.NotContains(t, ev.Text, `"up"`)
}

func TestWarnUnavailableMCPServersWithNoneUnavailable(t *testing.T) {
	t.Parallel()

	h, _, _ := newMCPTestHandler(t, "file:///work/repo", nil)
	tx := make(chan dialoguetui.MessageEvent, 1)
	h.warnUnavailableMCPServers(tx)
	assert.Empty(t, tx)
}

func TestConnectMCPServersWarnsOpenChats(t *testing.T) {
	t.Parallel()

	mux, guard := signInMux(t)
	signIn := serveMCP(t, mux, guard)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no MCP here", http.StatusBadRequest)
	}))
	t.Cleanup(broken.Close)
	user, err := runemcp.ParseServers(map[string]any{
		"linear": map[string]any{"type": "http", "url": signIn + "/mcp"},
		"broken": map[string]any{"type": "http", "url": broken.URL + "/mcp"},
		"up":     map[string]any{"type": "http", "url": serveMCP(t, http.NewServeMux(), nil) + "/mcp"},
	})
	require.NoError(t, err)
	workspace, err := runemcp.ParseServers(map[string]any{
		"untrusted": map[string]any{"type": "http", "url": "https://mcp.example.com/mcp"},
	})
	require.NoError(t, err)
	servers := runemcp.Merge(user, workspace)
	h, _, _ := newMCPTestHandler(t, "file:///work/repo", servers, 2)
	var interrupts atomic.Int32
	h.p = term.FuncInterrupter(func(context.Context) error {
		interrupts.Add(1)
		return nil
	})
	// Both chats open while the servers start, as chats restored at
	// launch do; one closes before the servers settle.
	open := openTestChat(h, "rolling-fox")
	closed := openTestChat(h, "quiet-owl")
	h.openChats.Delete("quiet-owl")

	h.connectMCPServers(servers)

	text := chatText(t, open)
	assert.Contains(t, text, "MCP server \"linear\" needs sign-in: run `agent mcp login linear`.")
	assert.Contains(t, text, "MCP server \"untrusted\" is unavailable: not trusted: run `agent mcp trust untrusted`")
	assert.Contains(t, text, `MCP server "broken" is unavailable:`)
	assert.Contains(t, text, "Run `agent mcp reconnect broken` to try again.")
	assert.NotContains(t, text, `"up"`, "servers that connect bring tools, not warnings")
	assert.NotContains(t, chatText(t, closed), "MCP server")
	assert.Positive(t, interrupts.Load(), "the warnings are drawn")
}

func TestMCPServerLaterTroubleWarnsOpenChats(t *testing.T) {
	t.Parallel()

	h, noti := newMCPHandlerInEveryState(t)
	chat := openTestChat(h, "rolling-fox")

	h.mcpManager.OnNeedsAuth("linear")
	h.mcpManager.OnServerExit("broken", "disconnected after 5 reconnect attempts: EOF")
	h.warnChatsAboutMCPServer("pending")
	h.warnChatsAboutMCPServer("up")

	text := chatText(t, chat)
	assert.Contains(t, text, "MCP server \"linear\" needs sign-in: run `agent mcp login linear`.")
	assert.Contains(t, text, "Run `agent mcp reconnect broken` to try again.")
	assert.NotContains(t, text, `"pending"`, "a server still starting settles in a later warning")
	assert.NotContains(t, text, `"up"`)
	assert.Contains(t, noteMessages(noti), "MCP server \"linear\" needs sign-in: run `agent mcp login linear`")
	assert.Contains(t, noteMessages(noti), `MCP server "broken" disconnected after 5 reconnect attempts: EOF`)
}

func TestWarnChatsAboutMCPServerRacesChatEvents(t *testing.T) {
	t.Parallel()

	h, _, _ := newMCPTestHandler(t, "file:///work/repo", nil)
	h.mcpManager.Register("untrusted", runemcp.ServerConfig{URL: "https://mcp.example.com/mcp"}, nil,
		errors.New("not trusted: run `agent mcp trust untrusted`"))
	chat := openTestChat(h, "rolling-fox")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, tx, _ := dialoguetui.Handler(ctx, chat.mu, chat.comp, term.FuncInterrupter(func(context.Context) error {
		return nil
	}))

	var wg sync.WaitGroup
	wg.Add(2)
	go debug.CapturePanicReport(func() {
		defer wg.Done()
		for range 50 {
			tx <- dialoguetui.MessageEvent{Type: dialoguetui.MessageEventWarning, Text: "from the agent"}
		}
	})
	go debug.CapturePanicReport(func() {
		defer wg.Done()
		for range 50 {
			h.warnChatsAboutMCPServer("untrusted")
		}
	})
	wg.Wait()
	close(tx)
	assert.Contains(t, chatText(t, chat), `MCP server "untrusted" is unavailable`)
}

func TestConnectMCPServers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cwd     string
		user    bool
		answers []int
		// want is the server's status after connecting, and wantNote and
		// wantWarning parts of a notification and of a server warning.
		want        runemcp.ServerStatus
		wantTool    bool
		wantNote    string
		wantWarning string
	}{
		{
			name:     "trusted workspace server",
			cwd:      "file:///work/repo",
			answers:  []int{0},
			want:     runemcp.StatusConnected,
			wantTool: true,
			wantNote: `MCP server "docs" is up: 1 tools available`,
		},
		{
			name:     "declined workspace server",
			cwd:      "file:///work/repo",
			answers:  []int{2},
			want:     runemcp.StatusError,
			wantNote: "MCP server \"docs\" is not connected: not trusted: run `agent mcp trust docs`",
		},
		{
			name:        "loopback server of a remote workspace",
			cwd:         "ssh://dev@build-box/work/repo",
			user:        true,
			want:        runemcp.StatusConnected,
			wantTool:    true,
			wantNote:    "which resolves on this machine, not the workspace host",
			wantWarning: "resolves on this machine, not the workspace host",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := serveMCP(t, http.NewServeMux(), nil)
			declared, err := runemcp.ParseServers(map[string]any{
				"docs": map[string]any{"type": "http", "url": base + "/mcp"},
			})
			require.NoError(t, err)
			servers := runemcp.Merge(nil, declared)
			if tt.user {
				servers = runemcp.Merge(declared, nil)
			}
			h, noti, _ := newMCPTestHandler(t, tt.cwd, servers, tt.answers...)

			h.connectMCPServers(servers)

			info := h.mcpManager.Servers()
			require.Len(t, info, 1)
			assert.Equal(t, tt.want, info[0].Status)
			_, ok := h.toolRegistry.Get("mcp__docs__search", "")
			assert.Equal(t, tt.wantTool, ok)
			assert.Condition(t, func() bool {
				for _, msg := range noteMessages(noti) {
					if strings.Contains(msg, tt.wantNote) {
						return true
					}
				}
				return false
			}, "notifications %q lack %q", noteMessages(noti), tt.wantNote)
			if tt.wantWarning != "" {
				require.Len(t, info[0].Warnings, 1)
				assert.Contains(t, info[0].Warnings[0], tt.wantWarning)
			}
		})
	}
}

// refusingExec records the commands it is asked to start, and starts
// none.
type refusingExec struct {
	mu    sync.Mutex
	calls []workspaceapi.Cmd
}

func (e *refusingExec) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, cmd)
	return 0, errors.New("refused")
}

func (*refusingExec) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }

func (*refusingExec) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (*refusingExec) Close() error { return nil }

func (e *refusingExec) started() []workspaceapi.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.calls)
}

func TestConnectMCPServersAsksBeforeStartingWorkspaceCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		user        bool
		answers     []int
		wantPrompts int
		wantStart   string
		wantError   string
	}{
		{
			name:        "trusted",
			answers:     []int{0},
			wantPrompts: 1,
			wantStart:   `MCP server "docs" from the workspace's .mcp.json`,
		},
		{
			name:        "declined",
			answers:     []int{2},
			wantPrompts: 1,
			wantError:   "not trusted: run `agent mcp trust docs`",
		},
		{
			name:      "from the user's config",
			user:      true,
			wantStart: `MCP server "docs" from your Rune config`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			declared, err := runemcp.ParseServers(map[string]any{
				"docs": map[string]any{"command": "docs-server", "args": []any{"--stdio"}},
			})
			require.NoError(t, err)
			servers := runemcp.Merge(nil, declared)
			if tt.user {
				servers = runemcp.Merge(declared, nil)
			}
			h, _, prompter := newMCPTestHandler(t, "file:///work/repo", servers, tt.answers...)
			exec := &refusingExec{}
			require.NoError(t, h.mcpManager.Close())
			h.mcpManager = runemcp.NewManager(exec, t.TempDir(), nil)
			h.wireMCPManager()

			h.connectMCPServers(servers)

			assert.Equal(t, tt.wantPrompts, prompter.prompts())
			started := exec.started()
			if tt.wantStart == "" {
				assert.Empty(t, started, "an untrusted command never reaches the executor")
				assert.Equal(t, tt.wantError, h.mcpManager.Servers()[0].Error)
				return
			}
			require.Len(t, started, 1)
			assert.Equal(t, "docs-server", started[0].Path)
			assert.Equal(t, tt.wantStart, started[0].Description)
		})
	}
}

func TestOpenURLRefusesNonWebSchemes(t *testing.T) {
	t.Parallel()

	opener := &signInOpener{errs: make(chan error, 1)}
	h := &aiEditorHandler{o: opener}
	err := h.openURL("file:///etc/passwd")
	require.Error(t, err)
	assert.Empty(t, opener.shown)
}

func TestLoadMCPServers(t *testing.T) {
	t.Parallel()

	userServers := map[string]any{
		"docs":   map[string]any{"url": "https://user.example.com/mcp"},
		"search": map[string]any{"url": "https://search.example.com/mcp"},
	}
	tests := []struct {
		name    string
		user    any
		mcpJSON string
		// want maps each server to its URL and whether the workspace
		// declared it.
		want     map[string]string
		wantFrom map[string]bool
		wantNote string
	}{
		{
			name:     "workspace wins a name clash",
			user:     userServers,
			mcpJSON:  `{"mcpServers": {"docs": {"url": "https://workspace.example.com/mcp"}}}`,
			want:     map[string]string{"docs": "https://workspace.example.com/mcp", "search": "https://search.example.com/mcp"},
			wantFrom: map[string]bool{"docs": true, "search": false},
		},
		{
			name:     "invalid .mcp.json keeps user servers",
			user:     userServers,
			mcpJSON:  `{"mcpServers": [`,
			want:     map[string]string{"docs": "https://user.example.com/mcp", "search": "https://search.example.com/mcp"},
			wantFrom: map[string]bool{"docs": false, "search": false},
			wantNote: ".mcp.json: invalid config",
		},
		{
			name:     "invalid user config keeps workspace servers",
			user:     map[string]any{"docs": map[string]any{"url": 42}},
			mcpJSON:  `{"mcpServers": {"docs": {"url": "https://workspace.example.com/mcp"}}}`,
			want:     map[string]string{"docs": "https://workspace.example.com/mcp"},
			wantFrom: map[string]bool{"docs": true},
			wantNote: "mcpServers: invalid config",
		},
		{
			name: "nothing configured",
			want: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.mcpJSON != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(tt.mcpJSON), 0o600))
			}
			raw := map[string]any{}
			if tt.user != nil {
				raw["mcpServers"] = tt.user
			}
			noti := &capturingNotifications{}

			servers := loadMCPServers(testLocalFS{root: dir}, dir, config.MapConfig(raw), noti)

			got := make(map[string]string, len(servers))
			for name, cfg := range servers {
				got[name] = cfg.URL
				assert.Equal(t, tt.wantFrom[name], cfg.FromWorkspace(), name)
			}
			assert.Equal(t, tt.want, got)
			notes := noteMessages(noti)
			if tt.wantNote == "" {
				assert.Empty(t, notes)
			} else {
				require.Len(t, notes, 1)
				assert.Contains(t, notes[0], tt.wantNote)
			}
		})
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
	goTo := func(uri, pos string) []string {
		return []string{"show " + uri, "editor " + uri, "cursor " + uri + " " + pos}
	}
	const configURI = "file:///workspace/project/config.go"
	tests := []struct {
		name        string
		cwd         string
		link        string
		showErr     error
		editorErr   error
		cursorErr   error
		clicks      int
		wantHandled bool
		wantCalls   []string
	}{
		{
			name:        "source link opens the file at its line",
			link:        "internal/config/config.go:99",
			wantHandled: true,
			wantCalls: []string{
				"show file:///workspace/project/internal/config/config.go",
				"editor file:///workspace/project/internal/config/config.go",
				"cursor file:///workspace/project/internal/config/config.go {X:0 Y:98}",
			},
		},
		{
			name:        "dot-prefixed source link to the first line",
			link:        "./config.go:1",
			wantHandled: true,
			wantCalls:   goTo(configURI, "{X:0 Y:0}"),
		},
		{
			name:        "line and column",
			link:        "./config.go:99:7",
			wantHandled: true,
			wantCalls:   goTo(configURI, "{X:6 Y:98}"),
		},
		{
			name:        "largest line and column",
			link:        "./config.go:2147483647:2147483647",
			wantHandled: true,
			wantCalls:   goTo(configURI, "{X:2147483646 Y:2147483646}"),
		},
		{
			name:        "absolute source link",
			link:        "/tmp/main.go:12:3",
			wantHandled: true,
			wantCalls:   goTo("file:///tmp/main.go", "{X:2 Y:11}"),
		},
		{
			name:        "file URL source link",
			link:        "file:///tmp/main.go:12",
			wantHandled: true,
			wantCalls:   goTo("file:///tmp/main.go", "{X:0 Y:11}"),
		},
		{
			name:        "source link in ssh workspace",
			cwd:         "ssh://alice@example.com:2222/work/project",
			link:        "internal/config/config.go:99:7",
			wantHandled: true,
			wantCalls: goTo(
				"ssh://alice@example.com:2222/work/project/internal/config/config.go",
				"{X:6 Y:98}",
			),
		},
		{
			name:        "source link in rune workspace",
			cwd:         "rune://server:7000/work/project",
			link:        "docs/README.md:3",
			wantHandled: true,
			wantCalls:   goTo("rune://server:7000/work/project/docs/README.md", "{X:0 Y:2}"),
		},
		{
			name:        "source link keeps escaping query and fragment",
			cwd:         "file:///workspace/my%20project",
			link:        "docs/a%20b%2525.go:4:2?view=raw#section",
			wantHandled: true,
			wantCalls: goTo(
				"file:///workspace/my%20project/docs/a%20b%2525.go?view=raw#section",
				"{X:1 Y:3}",
			),
		},
		{
			name:        "encoded colon is part of the file name",
			link:        "docs/notes%3A99",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/project/docs/notes%3A99"},
		},
		{
			name:        "location after an encoded colon",
			link:        "docs/notes%3A99:5",
			wantHandled: true,
			wantCalls:   goTo("file:///workspace/project/docs/notes%3A99", "{X:0 Y:4}"),
		},
		{
			name:        "encoded colon does not delimit a column",
			link:        "./main.go:10%3A5",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/project/main.go:10%3A5"},
		},
		{
			name:        "nonnumeric colon is part of the file name",
			link:        "docs/a:b.md",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/project/docs/a:b.md"},
		},
		{
			name:        "location is only read from the last segment",
			link:        "./docs:9/README.md",
			wantHandled: true,
			wantCalls:   []string{"show file:///workspace/project/docs:9/README.md"},
		},
		{
			name:        "https port and colon-number path are untouched",
			link:        "https://example.com:8443/src/main.go:99:7",
			wantHandled: true,
			wantCalls:   []string{"show https://example.com:8443/src/main.go:99:7"},
		},
		{
			name:        "http colon-number path is untouched",
			link:        "http://example.com:8080/main.go:99?a=1#L3",
			wantHandled: true,
			wantCalls:   []string{"show http://example.com:8080/main.go:99?a=1#L3"},
		},
		{
			name:        "zero line is rejected",
			link:        "./config.go:0",
			wantHandled: true,
			wantCalls:   []string{`open ./config.go:0: invalid source location ":0"`},
		},
		{
			name:        "zero column is rejected",
			link:        "./config.go:9:0",
			wantHandled: true,
			wantCalls:   []string{`open ./config.go:9:0: invalid source location ":9:0"`},
		},
		{
			name:        "negative line is rejected",
			link:        "./config.go:-1",
			wantHandled: true,
			wantCalls:   []string{`open ./config.go:-1: invalid source location ":-1"`},
		},
		{
			name:        "negative line with column is rejected",
			link:        "./config.go:-9:2",
			wantHandled: true,
			wantCalls:   []string{`open ./config.go:-9:2: invalid source location ":-9:2"`},
		},
		{
			name:        "signed column is rejected",
			link:        "./config.go:9:+2",
			wantHandled: true,
			wantCalls:   []string{`open ./config.go:9:+2: invalid source location ":9:+2"`},
		},
		{
			name:        "overflowing line is rejected",
			link:        "./config.go:2147483648",
			wantHandled: true,
			wantCalls: []string{
				`open ./config.go:2147483648: invalid source location ":2147483648"`,
			},
		},
		{
			name:        "overflowing column is rejected",
			link:        "./config.go:1:99999999999999999999",
			wantHandled: true,
			wantCalls: []string{
				`open ./config.go:1:99999999999999999999: ` +
					`invalid source location ":1:99999999999999999999"`,
			},
		},
		{
			name:        "location without a file is rejected",
			link:        "./:99",
			wantHandled: true,
			wantCalls:   []string{`open ./:99: source location ":99" names no file`},
		},
		{
			name:        "line and column without a file are rejected",
			link:        "docs/:99:1",
			wantHandled: true,
			wantCalls:   []string{`open docs/:99:1: source location ":99:1" names no file`},
		},
		{
			name:        "failed show stops before navigating",
			link:        "./config.go:99",
			showErr:     errors.New("permission denied"),
			wantHandled: true,
			wantCalls: []string{
				"show " + configURI,
				"open ./config.go:99: permission denied",
			},
		},
		{
			name:        "failed editor lookup stops before moving the cursor",
			link:        "./config.go:99",
			editorErr:   errors.New("handler not found"),
			wantHandled: true,
			wantCalls: []string{
				"show " + configURI,
				"editor " + configURI,
				"open ./config.go:99: go to 99:1: handler not found",
			},
		},
		{
			name:        "file shown outside the workspace reports failed navigation",
			cwd:         "ssh://alice@example.com/work/project",
			link:        "file:///tmp/main.go:3",
			editorErr:   errors.New("handler not found"),
			wantHandled: true,
			wantCalls: []string{
				"show file:///tmp/main.go",
				"editor file:///tmp/main.go",
				"open file:///tmp/main.go:3: go to 3:1: handler not found",
			},
		},
		{
			name:        "failed cursor move notifies",
			link:        "./config.go:99:7",
			cursorErr:   errors.New("line out of range"),
			wantHandled: true,
			wantCalls: append(
				goTo(configURI, "{X:6 Y:98}"),
				"open ./config.go:99:7: go to 99:7: line out of range",
			),
		},
		{
			name:        "repeated navigation to an open file",
			link:        "./config.go:99",
			clicks:      2,
			wantHandled: true,
			wantCalls: append(
				goTo(configURI, "{X:0 Y:98}"),
				goTo(configURI, "{X:0 Y:98}")...,
			),
		},
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
		{name: "bare scheme-like destination is declined", link: "config.go:99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lock := &dialogueLock{}
			io := &linkClickIO{
				lock:      lock,
				showErr:   tt.showErr,
				editorErr: tt.editorErr,
				cursorErr: tt.cursorErr,
			}
			cwd := tt.cwd
			if cwd == "" {
				cwd = "file:///workspace/project"
			}
			uri, err := workspaceapi.ParseURI(cwd)
			require.NoError(t, err)
			h := &aiEditorHandler{o: io, n: io, ed: io, cwd: uri}
			link, err := url.Parse(tt.link)
			require.NoError(t, err)
			original := *link

			onClick := h.dialogueConfig(lock).OnLinkClick
			for range max(tt.clicks, 1) {
				// The dialogue holds its lock while it calls OnLinkClick.
				lock.Lock()
				handled := onClick(link)
				lock.Unlock()
				assert.Equal(t, tt.wantHandled, handled)
			}
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

// linkClickIO is a resource opener, editor and notifications that record
// each call, marking the ones made while lock was held.
type linkClickIO struct {
	stubNotifications
	lock      *dialogueLock
	showErr   error
	editorErr error
	cursorErr error
	// panicsIn names the call that panics: "show", "editor" or "cursor".
	panicsIn string
	calls    []string
}

func (r *linkClickIO) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	r.record("open tab " + uri.String())
	return nil, errors.New("unexpected Open")
}

func (r *linkClickIO) Show(uri workspaceapi.URI) error {
	r.record("show " + uri.String())
	r.panicIn("show")
	return r.showErr
}

func (r *linkClickIO) Editor(uri workspaceapi.URI) (textapi.Handler, error) {
	r.record("editor " + uri.String())
	r.panicIn("editor")
	if r.editorErr != nil {
		return nil, r.editorErr
	}
	return cursorTestHandler{TestHandler: browsertest.NewTestHandler(), uri: uri}, nil
}

func (r *linkClickIO) CellView(textapi.Handler) textapi.CellView {
	r.record("cell view")
	return cursorTestView{}
}

func (r *linkClickIO) SetCursor(h textapi.Handler, pos term.Coordinates) error {
	r.record(fmt.Sprintf("cursor %s %+v", h.Resource(), pos))
	r.panicIn("cursor")
	return r.cursorErr
}

func (r *linkClickIO) Notify(
	_ browserapi.NotificationLevel, format string, args ...any,
) (string, error) {
	r.record(fmt.Sprintf(format, args...))
	return "", nil
}

func (r *linkClickIO) panicIn(call string) {
	if r.panicsIn == call {
		panic("host connection lost")
	}
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
		source = "[`GetGraphics`](internal/config/config.go:99)"
		config = "file:///workspace/project/internal/config/config.go"
	)
	tests := []struct {
		name string
		cwd  string
		link string
		// message is the rendered response, linking label. It defaults
		// to a runbook link to link.
		message   string
		label     string
		showErr   error
		editorErr error
		cursorErr error
		panicsIn  string
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
			panicsIn:  "show",
			wantCalls: []string{"show " + link},
		},
		{
			name:    "source link navigates unlocked",
			cwd:     "file:///workspace/project",
			message: "Graphics are read by " + source + ".",
			label:   "GetGraphics",
			wantCalls: []string{
				"show " + config,
				"editor " + config,
				"cursor " + config + " {X:0 Y:98}",
			},
		},
		{
			name:      "failed lookup notifies unlocked",
			cwd:       "file:///workspace/project",
			message:   source,
			label:     "GetGraphics",
			editorErr: errors.New("handler not found"),
			wantCalls: []string{
				"show " + config,
				"editor " + config,
				"open internal/config/config.go:99: go to 99:1: handler not found",
			},
		},
		{
			name:      "failed cursor move notifies unlocked",
			cwd:       "file:///workspace/project",
			message:   source,
			label:     "GetGraphics",
			cursorErr: errors.New("line out of range"),
			wantCalls: []string{
				"show " + config,
				"editor " + config,
				"cursor " + config + " {X:0 Y:98}",
				"open internal/config/config.go:99: go to 99:1: line out of range",
			},
		},
		{
			name:      "panicking lookup hands the lock back",
			cwd:       "file:///workspace/project",
			message:   source,
			label:     "GetGraphics",
			panicsIn:  "editor",
			wantCalls: []string{"show " + config, "editor " + config},
		},
		{
			name:     "panicking cursor move hands the lock back",
			cwd:      "file:///workspace/project",
			message:  source,
			label:    "GetGraphics",
			panicsIn: "cursor",
			wantCalls: []string{
				"show " + config,
				"editor " + config,
				"cursor " + config + " {X:0 Y:98}",
			},
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
				lock:      lock,
				showErr:   tt.showErr,
				editorErr: tt.editorErr,
				cursorErr: tt.cursorErr,
				panicsIn:  tt.panicsIn,
			}
			h := &aiEditorHandler{o: io, n: io, ed: io}
			if tt.cwd != "" {
				var err error
				h.cwd, err = workspaceapi.ParseURI(tt.cwd)
				require.NoError(t, err)
			}
			comp := h.newDialogueComponent(lock)
			dh, tx, _ := dialoguetui.Handler(context.Background(), lock, comp, term.NopInterrupter())
			defer close(tx)
			dh.Resize(width, height)
			message, label := tt.message, tt.label
			if message == "" {
				message, label = "See [the rollout runbook]("+tt.link+").", "the rollout runbook"
			}
			comp.AddReceiveMessage(message)
			w := term.NewStringWriter(width, height)
			dh.Draw(w)
			pos, ok := cellOf(w, width, label)
			require.True(t, ok, "link drawn")

			click := func() {
				dh.Handle(term.Event{
					Type: term.EventMouse, Key: term.MouseLeft, MouseX: pos.X, MouseY: pos.Y,
				})
			}
			if tt.panicsIn != "" {
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

// statusBarRow draws comp and returns its bottom row, the status bar.
func statusBarRow(t *testing.T, sc syncComponent) string {
	t.Helper()
	const width, height = 40, 8
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.comp.Resize(width, height)
	w := term.NewStringWriter(width, height)
	sc.comp.Draw(w)
	require.NoError(t, w.Flush())
	lines := strings.Split(w.String(), "\n")
	return strings.TrimSpace(lines[height-1])
}

func TestReloadStatusBar(t *testing.T) {
	modelLayout, err := dialoguetui.ParseStatusBarLayout(`{{ .Model }}`)
	require.NoError(t, err)
	agentConfig := func(statusBar map[string]any) config.Config {
		return config.MapConfig(map[string]any{"extensions": map[string]any{
			extensionID: map[string]any{"config": map[string]any{"status_bar": statusBar}},
		}})
	}
	tests := []struct {
		name    string
		host    config.Config
		wantErr string
		// wantRow is the open chat's bar after the reload; empty when
		// it has none.
		wantRow     string
		wantEnabled bool
		wantLayout  []dialoguetui.StatusBarComponent
	}{
		{
			name:        "the agent's own block",
			host:        agentConfig(map[string]any{"layout": `{{ .Effort }}`}),
			wantRow:     "high",
			wantEnabled: true,
			wantLayout:  mustParseStatusBarLayout(t, `{{ .Effort }}`),
		},
		{
			name:        "no block of its own",
			host:        config.MapConfig(map[string]any{}),
			wantEnabled: true,
			wantRow:     "IDLE",
		},
		{
			name: "a block that turns the bar off",
			host: agentConfig(map[string]any{"enabled": false}),
		},
		{
			name:        "a config that could not be fetched",
			host:        config.ErrConfig(errors.New("closed")),
			wantErr:     "closed",
			wantRow:     "opus",
			wantEnabled: true,
			wantLayout:  modelLayout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initial := dialoguetui.StatusBarConfig{
				Enabled: true, Layout: modelLayout, Storage: storagestub.NewInMemoryService(),
			}
			h := &aiEditorHandler{n: stubNotifications{}, p: term.NopInterrupter()}
			h.cfg.StatusBar = initial
			open := syncComponent{
				mu:   new(sync.Mutex),
				comp: dialoguetui.NewComponent(dialoguetui.ComponentConfig{StatusBar: initial}),
			}
			t.Cleanup(func() { assert.NoError(t, open.comp.Close()) })
			open.setStatusBarState(func(s *dialoguetui.StatusBarState) { s.Model, s.Effort = "opus", "high" })
			h.openChats.Store("open", open)
			h.openChats.Store("opening", struct{}{})

			err := h.reloadStyle(context.Background(), tt.host)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.wantRow == "" {
				assert.Zero(t, open.comp.StatusBarState(), "the open chat has no bar")
			} else {
				assert.Contains(t, statusBarRow(t, open), tt.wantRow)
			}
			next := h.dialogueConfig(new(sync.Mutex)).StatusBar
			assert.Equal(t, tt.wantEnabled, next.Enabled, "a chat opened next")
			assert.Equal(t, tt.wantLayout, next.Layout, "a chat opened next")
			assert.NotNil(t, next.Storage, "the bar keeps caching its images")
		})
	}
}

func TestReloadStyleInputBoxFrame(t *testing.T) {
	const width, height = 32, 6
	// The asciiart writer draws a gray 128 pixel as 'a', a gray 40 one as
	// '_' and a white one as '@'.
	const opened = "                                \n" +
		"                                \n" +
		"                                \n" +
		"  a────────────────────────a    \n" +
		"  │                        │    \n" +
		"  └────────────────────────┘    "
	const unframed = "                                \n" +
		"                                \n" +
		"                                \n" +
		"  ┌────────────────────────┐    \n" +
		"  │                        │    \n" +
		"  └────────────────────────┘    "
	host := func(charset map[string]any) config.Config {
		own := map[string]any{"status_bar": map[string]any{"enabled": false}}
		if charset != nil {
			own["input_box_frame_charset"] = charset
		}
		return config.MapConfig(map[string]any{"extensions": map[string]any{
			extensionID: map[string]any{"config": own},
		}})
	}
	openWith := func(gray string) config.Config {
		return host(map[string]any{
			"topleft":  "╓",
			"graphics": map[string]any{"topleft": gray, "topright": gray},
		})
	}
	tests := []struct {
		name string
		// edit draws the gray image the chat opened with in gray 40
		// before the reload.
		edit bool
		// host is the config reloaded given the URIs of a gray and a
		// white image.
		host     func(gray, white string) config.Config
		expected string
	}{
		{
			name: "an edited image is read again",
			edit: true,
			host: func(gray, _ string) config.Config { return openWith(gray) },
			expected: "                                \n" +
				"                                \n" +
				"                                \n" +
				"  _────────────────────────_    \n" +
				"  │                        │    \n" +
				"  └────────────────────────┘    ",
		},
		{
			name: "runes and images over the default frame",
			host: func(_, white string) config.Config {
				return host(map[string]any{
					"topleft": "╔",
					"graphics": map[string]any{
						"bottomright": white, "bottomleft": "file:///missing.png",
					},
				})
			},
			expected: "                                \n" +
				"                                \n" +
				"                                \n" +
				"  ╔────────────────────────┐    \n" +
				"  │                        │    \n" +
				"  └────────────────────────@    ",
		},
		{
			name: "an invalid image keeps the default frame",
			host: func(_, _ string) config.Config {
				return host(map[string]any{
					"topleft": "╓", "graphics": map[string]any{"topleft": "ftp://x/y.png"},
				})
			},
			expected: unframed,
		},
		{
			name:     "no charset of its own",
			host:     func(_, _ string) config.Config { return host(nil) },
			expected: unframed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fileURI := func(name string, c color.Color) string {
				path := filepath.Join(dir, name)
				writeFramePNG(t, path, c)
				return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
			}
			gray := fileURI("gray.png", color.Gray{Y: 128})
			white := fileURI("white.png", color.White)
			own, err := ownConfig(openWith(gray))
			require.NoError(t, err)

			interrupts := make(chan struct{}, 1)
			p := term.FuncInterrupter(func(context.Context) error {
				select {
				case interrupts <- struct{}{}:
				default:
				}
				return nil
			})
			h := &aiEditorHandler{n: stubNotifications{}, p: p}
			h.cfg = dialoguetui.DefaultComponentConfig()
			h.cfg.InputBox.GraphicsStorage = storagestub.NewInMemoryService()
			h.cfg, _ = dialoguetui.ReadStyle(context.Background(), h.cfg, own, config.NopConfig(), stubNotifications{})
			mu := new(sync.Mutex)
			comp := dialoguetui.NewComponent(h.dialogueConfig(mu))
			t.Cleanup(func() { assert.NoError(t, comp.Close()) })
			dh, tx, _ := dialoguetui.Handler(context.Background(), mu, comp, p)
			defer close(tx)
			h.openChats.Store("open", syncComponent{mu: mu, comp: comp})
			dh.Resize(width, height)
			awaitChat(t, dh, width, height, interrupts, opened)

			if tt.edit {
				writeFramePNG(t, filepath.Join(dir, "gray.png"), color.Gray{Y: 40})
			}
			require.NoError(t, h.reloadStyle(context.Background(), tt.host(gray, white)))
			awaitChat(t, dh, width, height, interrupts, tt.expected)

			nextMu := new(sync.Mutex)
			next := dialoguetui.NewComponent(h.dialogueConfig(nextMu))
			t.Cleanup(func() { assert.NoError(t, next.Close()) })
			nextH, nextTx, _ := dialoguetui.Handler(context.Background(), nextMu, next, p)
			defer close(nextTx)
			nextH.Resize(width, height)
			awaitChat(t, nextH, width, height, interrupts, tt.expected)
		})
	}
}

func TestReloadStyleInputBoxColors(t *testing.T) {
	const width, height = 32, 6
	host := func(own map[string]any) config.Config {
		own["status_bar"] = map[string]any{"enabled": false}
		return config.MapConfig(map[string]any{"extensions": map[string]any{
			extensionID: map[string]any{"config": own},
		}})
	}
	// colors draws h with "h" typed in its compose box and returns the
	// background of the box's frame and of its input.
	colors := func(t *testing.T, h tui.Handler) (frame, input term.Color) {
		t.Helper()
		h.Resize(width, height)
		h.Handle(term.Event{Type: term.EventKey, Ch: 'h'})
		w := term.NewStringWriter(width, height)
		h.Draw(w)
		var sawFrame, sawInput bool
		for _, c := range w.Cells() {
			switch c.Ch {
			case '┌':
				frame, sawFrame = c.Bg, true
			case 'h':
				input, sawInput = c.Bg, true
			}
		}
		require.True(t, sawFrame && sawInput, "the compose box is drawn")
		return frame, input
	}
	tests := []struct {
		name      string
		reloaded  map[string]any
		wantFrame term.Color
		wantInput term.Color
	}{
		{
			name:      "a new input background",
			reloaded:  map[string]any{"input_background_color": "blue"},
			wantFrame: term.ColorBlue,
			wantInput: term.ColorBlue,
		},
		{
			name:      "the input box's background",
			reloaded:  map[string]any{"input_box_attr": map[string]any{"bg": "teal"}},
			wantFrame: term.ColorTeal,
			wantInput: term.ColorTeal,
		},
		{
			name:      "no input background",
			reloaded:  map[string]any{"input_box_frame_attr": map[string]any{"bg": "navy"}},
			wantFrame: term.ColorNavy,
			wantInput: term.ColorDefault,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &aiEditorHandler{n: stubNotifications{}, p: term.NopInterrupter()}
			h.cfg = dialoguetui.DefaultComponentConfig()
			h.cfg.InputBackgroundColor = term.ColorRed
			mu := new(sync.Mutex)
			comp := dialoguetui.NewComponent(h.dialogueConfig(mu))
			t.Cleanup(func() { assert.NoError(t, comp.Close()) })
			dh, tx, _ := dialoguetui.Handler(context.Background(), mu, comp, h.p)
			defer close(tx)
			h.openChats.Store("open", syncComponent{mu: mu, comp: comp})
			frame, input := colors(t, dh)
			require.Equal(t, term.ColorRed, frame, "opened")
			require.Equal(t, term.ColorRed, input, "opened")

			require.NoError(t, h.reloadStyle(context.Background(), host(tt.reloaded)))
			frame, input = colors(t, dh)
			assert.Equal(t, tt.wantFrame, frame, "the open chat's frame")
			assert.Equal(t, tt.wantInput, input, "the open chat's input")

			nextMu := new(sync.Mutex)
			next := dialoguetui.NewComponent(h.dialogueConfig(nextMu))
			t.Cleanup(func() { assert.NoError(t, next.Close()) })
			nextH, nextTx, _ := dialoguetui.Handler(context.Background(), nextMu, next, h.p)
			defer close(nextTx)
			frame, input = colors(t, nextH)
			assert.Equal(t, tt.wantFrame, frame, "a chat opened next: frame")
			assert.Equal(t, tt.wantInput, input, "a chat opened next: input")
		})
	}
}

// awaitChat redraws h on every interrupt until it draws expected with
// the asciiart writer; the frame images load in the background.
func awaitChat(
	t *testing.T, h tui.Handler, width, height int,
	interrupts <-chan struct{}, expected string,
) {
	t.Helper()
	w := asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())
	timeout := time.After(10 * time.Second)
	for {
		require.NoError(t, w.Clear(term.Attributes{}))
		h.Draw(w)
		require.NoError(t, w.Flush())
		if w.String() == expected {
			return
		}
		select {
		case <-interrupts:
		case <-time.After(10 * time.Millisecond):
		case <-timeout:
			require.Equal(t, expected, w.String())
		}
	}
}

func writeFramePNG(t *testing.T, path string, c color.Color) {
	t.Helper()
	img := stdimage.NewNRGBA(stdimage.Rect(0, 0, 1, 1))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

func mustParseStatusBarLayout(t *testing.T, layout string) []dialoguetui.StatusBarComponent {
	t.Helper()
	parsed, err := dialoguetui.ParseStatusBarLayout(layout)
	require.NoError(t, err)
	return parsed
}

func TestOwnConfig(t *testing.T) {
	own := map[string]any{"status_bar": map[string]any{"enabled": false}}
	tests := []struct {
		name    string
		host    config.Config
		want    config.Config
		wantErr string
	}{
		{
			name: "the agent's block",
			host: config.MapConfig(map[string]any{
				"editor":     map[string]any{"mode": "modal"},
				"extensions": map[string]any{extensionID: map[string]any{"config": own}},
			}),
			want: config.MapConfig(own),
		},
		{name: "no extensions", host: config.MapConfig(map[string]any{}), want: config.NopConfig()},
		{
			name: "no config of its own",
			host: config.MapConfig(map[string]any{
				"extensions": map[string]any{extensionID: map[string]any{"path": "/bin/rune-agent"}},
			}),
			want: config.NopConfig(),
		},
		{name: "a config that could not be fetched", host: config.ErrConfig(errors.New("closed")), wantErr: "closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ownConfig(tt.host)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWatchReloadFetchesTheConfigOnEachSignal(t *testing.T) {
	h := &aiEditorHandler{n: stubNotifications{}, p: term.NopInterrupter()}
	h.cfg.StatusBar = dialoguetui.StatusBarConfig{Enabled: true}
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan os.Signal)
	fetched := make(chan struct{}, 2)
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		h.watchReload(ctx, reload, func(context.Context) config.Config {
			fetched <- struct{}{}
			return config.MapConfig(map[string]any{"extensions": map[string]any{
				extensionID: map[string]any{"config": map[string]any{
					"status_bar": map[string]any{"enabled": false},
				}},
			}})
		})
	})

	for range 2 {
		reload <- os.Interrupt
		<-fetched
	}
	cancel()
	<-done
	assert.False(t, h.dialogueConfig(new(sync.Mutex)).StatusBar.Enabled)
}
