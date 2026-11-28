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

package llmconsole

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llmrouter"
	"unstable.build/rune/internal/llm/llmsearch"
)

// newRouterForTest constructs a router with the default llm.Config, a
// fresh local registry and a search service with the fake embedder.
func newRouterForTest(t *testing.T) *llmrouter.Router {
	t.Helper()
	reg := newRegistryForTest(t)
	search := llmsearch.New(llmsearch.DefaultConfig(), llmsearch.NewFakeEmbedderFactory(), reg, nopCloser{})
	r := llmrouter.New(llm.DefaultConfig(), reg, storagestub.NewInMemoryService(), fakeLocalBackend{}, search)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// fakeLocalBackend is a no-op llmapi.Service stand-in for the local
// (llama-server) backend the router now requires at construction.
type fakeLocalBackend struct{}

func (fakeLocalBackend) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

func (fakeLocalBackend) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

func (fakeLocalBackend) CreateCompletion(
	context.Context, llmapi.ModelEntry, llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	return iterator.FromSlice[llmapi.Event](nil), nil
}

func (fakeLocalBackend) CountTokens(llmapi.ModelEntry, []llmapi.Message) (int, error) {
	return 0, nil
}

func (fakeLocalBackend) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice[llmapi.ModelEntry](nil)
}

func (fakeLocalBackend) GetModel(
	context.Context, llmapi.ModelEntry,
) (llmapi.ModelEntry, error) {
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

// newRegistryForTest returns a fresh llamacpp.Registry rooted in a temp
// directory.
func newRegistryForTest(t *testing.T) *llamacpp.Registry {
	t.Helper()
	r, err := llamacpp.NewRegistry(t.TempDir())
	require.NoError(t, err)
	return r
}

// stubStorageForTest returns an in-memory storage service.
func stubStorageForTest(t *testing.T) storageapi.Service {
	t.Helper()
	return storagestub.NewInMemoryService()
}

// newHandlerForTest constructs a Handler with stub dependencies.
func newHandlerForTest(t *testing.T) *Handler {
	t.Helper()
	router := newRouterForTest(t)
	return New(Config{
		Service:          router,
		Router:           router,
		LocalRegistry:    router.LocalRegistry(),
		Storage:          stubStorageForTest(t),
		Notifications:    &stubNotifications{},
		ScheduleNextTick: syncSchedule,
		Prompter:         &fakePrompter{},
	})
}

// syncSchedule runs scheduled work inline so tests need not pump an
// event loop.
func syncSchedule(fn func()) bool {
	fn()
	return true
}

// stubNotifications records notifications for assertions.
type stubNotifications struct {
	notes []stubNote
}

type stubNote struct {
	level browserapi.NotificationLevel
	msg   string
}

func (s *stubNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	s.notes = append(s.notes, stubNote{level: level, msg: fmt.Sprintf(msg, args...)})
	return "", nil
}

func (s *stubNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return s.Notify(level, msg, args...)
}

func (s *stubNotifications) UpdateNotificationProgress(_, _ string, _, _ int64) error {
	return nil
}

type stubWindow uint64

func (w stubWindow) WindowID() uint64 { return uint64(w) }

// fakePrompter opens real handler.Prompts that tests type into with
// keys. Like a browser, it closes a prompt once the prompt exits.
type fakePrompter struct {
	configs []browserapi.PromptConfig
	prompts []*handler.Prompt
}

func (p *fakePrompter) Prompt(
	cfg browserapi.PromptConfig, h handler.PromptHandler,
) (browserapi.Window, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	fields := make([]handler.PromptField, len(cfg.Fields))
	for i, f := range cfg.Fields {
		fields[i] = handler.PromptField{
			Question: f.Question, Answer: f.Answer, Secret: f.Secret, Mandatory: f.Mandatory,
		}
	}
	p.configs = append(p.configs, cfg)
	p.prompts = append(p.prompts, handler.NewPrompt(handler.PromptConfig{
		PromptConfig:   component.PromptConfig{Message: cfg.Message, Options: cfg.Options},
		PromptHandler:  h,
		Fields:         fields,
		OptionBindings: cfg.Bindings,
	}))
	return stubWindow(len(p.prompts)), nil
}

// keys types keys, see term.ParseKeys, into the last prompt opened,
// dropping those after it exits, and reports whether it exited.
func (p *fakePrompter) keys(t *testing.T, keys string) (exited bool) {
	t.Helper()
	require.NotEmpty(t, p.prompts, "no prompt is open")
	prompt := p.prompts[len(p.prompts)-1]
	evs, err := term.ParseKeys(keys)
	require.NoError(t, err)
	for _, k := range evs {
		if exit, _ := prompt.Handle(term.Event{Type: term.EventKey, Key: k.Key, Ch: k.Ch, Mod: k.Mod}); exit {
			require.NoError(t, prompt.Close())
			return true
		}
	}
	return false
}

func TestManualHasRequiredSubcommands(t *testing.T) {
	m := Manual()
	assert.Equal(t, "models", m.Name)

	subs := make(map[string]bool)
	for _, c := range m.Commands {
		subs[c.Name] = true
	}
	assert.True(t, subs["providers"], "missing providers")
	assert.True(t, subs["local"], "missing local")

	var providers, local []string
	for _, c := range m.Commands {
		switch c.Name {
		case "providers":
			for _, sc := range c.Commands {
				if sc.Name == "codex" {
					for _, ss := range sc.Commands {
						providers = append(providers, ss.Name)
					}
				}
			}
		case "local":
			for _, sc := range c.Commands {
				local = append(local, sc.Name)
			}
		}
	}
	assert.ElementsMatch(t, []string{"login", "status"}, providers)
	assert.ElementsMatch(t, []string{"list", "download", "delete"}, local)
}

func TestHandleCommandUnknownSubcommandErrors(t *testing.T) {
	h := newHandlerForTest(t)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models", Args: []string{"nope"}},
		nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command")
}

func TestHandleCommandNoArgsShowsUsage(t *testing.T) {
	h := newHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models", Args: nil},
		nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	v, ok := it.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestCompleteTopLevel(t *testing.T) {
	h := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), "models", nil)
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"providers", "local", "alias", "help"}, names)
}

func TestCompleteFiltersByPrefix(t *testing.T) {
	h := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), "models", []string{"pr"})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"providers"}, names)
}

func TestNewPanicsOnNilRouter(t *testing.T) {
	defer func() {
		assert.NotNil(t, recover(), "expected panic for nil router")
	}()
	_ = New(Config{Service: nil, LocalRegistry: newRegistryForTest(t), Storage: stubStorageForTest(t)})
}
