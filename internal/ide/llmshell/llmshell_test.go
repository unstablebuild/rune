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

package llmshell

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
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llmrouter"
)

// newRouterForTest constructs a router with the default llm.Config and a
// fresh temp directory for the local registry.
func newRouterForTest(t *testing.T) *llmrouter.Router {
	t.Helper()
	r, err := llmrouter.New(llm.DefaultConfig(), t.TempDir(),
		storagestub.NewInMemoryService(), fakeLocalBackend{})
	require.NoError(t, err)
	return r
}

// fakeLocalBackend is a no-op llmapi.Service stand-in for the local
// (llama-server) backend the router now requires at construction.
type fakeLocalBackend struct{}

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
		WindowManager:    &stubWindowManager{},
		Notifications:    &stubNotifications{},
		ScheduleNextTick: syncSchedule,
		PromptOpener:     &stubPromptOpener{},
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

// stubWindowManager captures the most recent floating handler so tests
// can drive it.
type stubWindowManager struct {
	lastFloating browserapi.Floating
	floatErr     error
}

func (s *stubWindowManager) Focus() (browserapi.Window, error) { return nil, nil }
func (s *stubWindowManager) Split(
	browserapi.Orientation, browserapi.Window, browserapi.Handler,
) (browserapi.Window, error) {
	return nil, nil
}
func (s *stubWindowManager) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	if s.floatErr != nil {
		return nil, s.floatErr
	}
	s.lastFloating = h
	return stubWindow(1), nil
}
func (s *stubWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (s *stubWindowManager) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}
func (s *stubWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error {
	return nil
}
func (s *stubWindowManager) CloseWindow(browserapi.Window) error       { return nil }
func (s *stubWindowManager) SetTabName(workspaceapi.URI, string) error { return nil }

func (s *stubWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

type stubWindow uint64

func (w stubWindow) WindowID() uint64 { return uint64(w) }

// stubPromptOpener records the most recent prompt and, when selectIdx is
// non-negative, immediately drives that option's handler so tests can
// exercise the confirm flow without an event loop.
type stubPromptOpener struct {
	called    bool
	message   string
	options   []string
	bindings  []term.KeyComb
	handler   handler.PromptHandler
	selectIdx int
}

func (s *stubPromptOpener) Prompt(
	message string, options []string,
	bindings []term.KeyComb,
	promptHandler handler.PromptHandler,
) browser.Window {
	s.called = true
	s.message = message
	s.options = options
	s.bindings = bindings
	s.handler = promptHandler
	if s.selectIdx >= 0 && s.selectIdx < len(options) {
		promptHandler.OnSelect(s.selectIdx, options[s.selectIdx])
	}
	return nil
}

// TestManualHasRequiredSubcommands verifies the public command tree
// matches what the plan promised: `models providers <codex> <login|status>`
// and `models local <list|download|delete>`.
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

// TestHandleCommandUnknownSubcommandErrors verifies that the parent
// dispatcher returns an error for unknown subcommands rather than
// silently delegating.
func TestHandleCommandUnknownSubcommandErrors(t *testing.T) {
	h := newHandlerForTest(t)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models", Args: []string{"nope"}},
		nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command")
}

// TestHandleCommandNoArgsShowsUsage returns a help block instead of
// an error when called with no arguments.
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

// TestCompleteTopLevel returns the top-level subcommands.
func TestCompleteTopLevel(t *testing.T) {
	h := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), "models", nil)
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"providers", "local", "alias", "help"}, names)
}

// TestCompleteFiltersByPrefix filters when a partial first arg is
// supplied.
func TestCompleteFiltersByPrefix(t *testing.T) {
	h := newHandlerForTest(t)
	it, err := h.Complete(context.Background(), "models", []string{"pr"})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"providers"}, names)
}

// TestNewPanicsOnNilRouter verifies New refuses to build with a nil
// router.
func TestNewPanicsOnNilRouter(t *testing.T) {
	defer func() {
		assert.NotNil(t, recover(), "expected panic for nil router")
	}()
	_ = New(Config{Service: nil, LocalRegistry: newRegistryForTest(t), Storage: stubStorageForTest(t)})
}
