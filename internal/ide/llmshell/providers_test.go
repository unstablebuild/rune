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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/llm/claude"
	"unstable.build/rune/internal/llm/llmrouter"
)

// newProvidersHandlerForTest builds a providersHandler wired to a real
// router (for the key store) plus UI stubs.
func newProvidersHandlerForTest(t *testing.T) (
	*providersHandler, *stubWindowManager, *stubNotifications, *stubPromptOpener,
) {
	t.Helper()
	wm := &stubWindowManager{}
	notifs := &stubNotifications{}
	prompt := &stubPromptOpener{selectIdx: -1}
	h := newProvidersHandler(providersConfig{
		storage:  storagestub.NewInMemoryService(),
		router:   newRouterForTest(t),
		wm:       wm,
		notifs:   notifs,
		schedule: syncSchedule,
		prompt:   prompt,
	})
	h.spawn = func(fn func()) { fn() }
	return h, wm, notifs, prompt
}

func TestProvidersNilDependencyPanics(t *testing.T) {
	defer func() {
		assert.NotNil(t, recover(), "expected panic for nil storage")
	}()
	router := newRouterForTest(t)
	_ = New(Config{
		Service:          router,
		Router:           router,
		LocalRegistry:    newRegistryForTest(t),
		Storage:          nil,
		WindowManager:    &stubWindowManager{},
		Notifications:    &stubNotifications{},
		ScheduleNextTick: syncSchedule,
	})
}

// renderedOnce asserts that the command produced exactly one rendered
// (markdown) responsive and no error, returning that single component.
func renderedOnce(
	t *testing.T, it iterator.Iterator[component.Responsive], err error,
) component.Responsive {
	t.Helper()
	require.NoError(t, err)
	got, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, got, 1)
	return got[0]
}

func TestProvidersNoArgsShowsHelp(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: nil}, nil)
	renderedOnce(t, it, err)
}

func TestProvidersUnknownProviderShowsHelp(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: []string{"bogus", "status"}}, nil)
	renderedOnce(t, it, err)
}

func TestProvidersCodexNoArgsShowsHelp(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: []string{"codex"}}, nil)
	renderedOnce(t, it, err)
}

func TestProvidersClaudeNoArgsShowsHelp(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: []string{"claude"}}, nil)
	renderedOnce(t, it, err)
}

func TestProvidersClaudeStatusDispatches(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: []string{"claude", "status"}}, nil)
	renderedOnce(t, it, err)
}

func TestFormatClaudeStatusUsageCreditNote(t *testing.T) {
	out := formatClaudeStatus(claude.AuthStatus{
		Authenticated: true,
		Email:         "dev@example.com",
		PlanType:      "max_20x",
	})
	assert.Contains(t, out, "dev@example.com")
	assert.Contains(t, out, "Agent-SDK credit")
	assert.Contains(t, out, "Settings > Usage")
}

func TestProvidersHostedNoArgsShowsHelp(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: []string{"openai"}}, nil)
	renderedOnce(t, it, err)
}

func TestProvidersHostedUnknownSubcommandShowsHelp(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: []string{"openai", "frobnicate"}}, nil)
	renderedOnce(t, it, err)
}

func TestProvidersHostedStatusEmpty(t *testing.T) {
	h, _, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "providers", Args: []string{"anthropic", "status"}}, nil)
	require.NoError(t, err)
	got, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestProvidersHostedUseAndRemove(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := newProvidersHandlerForTest(t)
	require.NoError(t, h.router.AddProviderKey(ctx, llmrouter.ProviderOpenAI, "work", "k1", ""))
	require.NoError(t, h.router.AddProviderKey(ctx, llmrouter.ProviderOpenAI, "home", "k2", ""))

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "use", "home"}}, nil)
	require.NoError(t, err)
	active, err := h.router.ProviderKeyActiveName(ctx, llmrouter.ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "home", active)

	_, err = h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "remove", "home"}}, nil)
	require.NoError(t, err)
	names, err := h.router.ProviderKeyNames(ctx, llmrouter.ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, names)

	_, err = h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "use"}}, nil)
	require.NoError(t, err, "use without a name renders help, not an error")
}

func TestProvidersComplete(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := newProvidersHandlerForTest(t)
	require.NoError(t, h.router.AddProviderKey(ctx, llmrouter.ProviderOpenAI, "work", "k1", ""))
	require.NoError(t, h.router.AddProviderKey(ctx, llmrouter.ProviderOpenAI, "home", "k2", ""))

	level1, err := h.Complete(ctx, "providers", nil)
	require.NoError(t, err)
	names, err := iterator.ToSlice(ctx, level1)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{"codex", "claude", "openai", "anthropic", "gemini", "bedrock"}, names)

	codexSubs, err := h.Complete(ctx, "providers", []string{"codex", ""})
	require.NoError(t, err)
	names, err = iterator.ToSlice(ctx, codexSubs)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"login", "status"}, names)

	claudeSubs, err := h.Complete(ctx, "providers", []string{"claude", ""})
	require.NoError(t, err)
	names, err = iterator.ToSlice(ctx, claudeSubs)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"login", "status"}, names)

	hostedSubs, err := h.Complete(ctx, "providers", []string{"openai", ""})
	require.NoError(t, err)
	names, err = iterator.ToSlice(ctx, hostedSubs)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"add", "remove", "use", "status"}, names)

	keyNames, err := h.Complete(ctx, "providers", []string{"openai", "use", ""})
	require.NoError(t, err)
	names, err = iterator.ToSlice(ctx, keyNames)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"home", "work"}, names)

	addNoNames, err := h.Complete(ctx, "providers", []string{"openai", "add", ""})
	require.NoError(t, err)
	names, err = iterator.ToSlice(ctx, addNoNames)
	require.NoError(t, err)
	assert.Empty(t, names, "add does not complete from stored names")
}

func TestProviderAddNoNameShowsHelp(t *testing.T) {
	h, wm, _, _ := newProvidersHandlerForTest(t)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "models providers", Args: []string{"openai", "add"}}, nil)
	renderedOnce(t, it, err)
	assert.Nil(t, wm.lastFloating, "no prompt should open without a key name")
}

func TestHostedProviderHelpIsRich(t *testing.T) {
	man, ok := providerManual(llmrouter.ProviderOpenAI)
	require.True(t, ok)
	help := usageMarkdown(man)

	assert.Contains(t, help, "## `models providers openai`")
	assert.Contains(t, help, "**Usage:** `models providers openai")
	assert.Contains(t, help, "### Subcommands")
	assert.Contains(t, help, "`add <name>`")
	assert.Contains(t, help, "`remove <name>`")
	assert.Contains(t, help, "`use <name>`")
	assert.Contains(t, help, "tests it against the provider")
	assert.Contains(t, help, "### Examples")
	assert.Contains(t, help, "models providers openai add work")
}

// submitKey drives the floating inputbox to type key and press Enter.
func submitKey(t *testing.T, fl browserapi.Floating, key string) {
	t.Helper()
	for _, r := range key {
		fl.Handle(term.Event{Type: term.EventKey, Ch: r})
	}
	fl.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
}

func TestProviderAddVerifySuccessStores(t *testing.T) {
	ctx := context.Background()
	h, wm, notifs, _ := newProvidersHandlerForTest(t)
	h.verify = func(context.Context, string, string, string) error { return nil }

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, wm.lastFloating, "expected a floating prompt to open")

	submitKey(t, wm.lastFloating, "sk-secret")

	active, err := h.router.ProviderKeyActive(ctx, llmrouter.ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", active)
	require.NotEmpty(t, notifs.notes)
	assert.Equal(t, browserapi.LevelSuccess, notifs.notes[len(notifs.notes)-1].level)
}

func TestProviderAddBedrockRequiresRegion(t *testing.T) {
	ctx := context.Background()
	h, wm, _, _ := newProvidersHandlerForTest(t)
	verifyCalled := false
	h.verify = func(context.Context, string, string, string) error {
		verifyCalled = true
		return nil
	}

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"bedrock", "add", "work"}}, nil)
	require.NoError(t, err)
	assert.Nil(t, wm.lastFloating, "no key prompt without a region")
	assert.False(t, verifyCalled)
}

func TestProviderAddBedrockStoresRegionWithKey(t *testing.T) {
	ctx := context.Background()
	h, wm, _, _ := newProvidersHandlerForTest(t)
	var verifiedRegion string
	h.verify = func(_ context.Context, _, _, region string) error {
		verifiedRegion = region
		return nil
	}

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"bedrock", "add", "work", "eu-west-1"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, wm.lastFloating)

	submitKey(t, wm.lastFloating, "bedrock-api-key-abc")

	assert.Equal(t, "eu-west-1", verifiedRegion, "verification must probe the key's region")
	regions, err := h.router.ProviderKeyRegions(ctx, llmrouter.ProviderBedrock)
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", regions["work"])
}

func TestProviderAddTrimsWhitespace(t *testing.T) {
	ctx := context.Background()
	h, wm, _, _ := newProvidersHandlerForTest(t)
	var verified string
	h.verify = func(_ context.Context, _, key, _ string) error {
		verified = key
		return nil
	}

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, wm.lastFloating)

	submitKey(t, wm.lastFloating, "  sk-secret  ")

	assert.Equal(t, "sk-secret", verified, "verification must use the trimmed key")
	active, err := h.router.ProviderKeyActive(ctx, llmrouter.ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", active, "stored key must be trimmed")
}

func TestProviderAddWhitespaceOnlyKeyAborts(t *testing.T) {
	ctx := context.Background()
	h, wm, _, _ := newProvidersHandlerForTest(t)
	verifyCalled := false
	h.verify = func(context.Context, string, string, string) error {
		verifyCalled = true
		return nil
	}

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	submitKey(t, wm.lastFloating, "   ")

	assert.False(t, verifyCalled, "whitespace-only key must not be verified")
	_, err = h.router.ProviderKeyActive(ctx, llmrouter.ProviderOpenAI)
	require.ErrorIs(t, err, llmrouter.ErrAPIKeyNotSet)
}

// drainDone reports a channel that closes once it has been fully drained
// (Next returned false). Used to observe when providerAdd's completion
// iterator resolves.
func drainDone(
	t *testing.T, it iterator.Iterator[component.Responsive],
) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := context.Background()
		for {
			if _, ok := it.Next(ctx); !ok {
				return
			}
		}
	}()
	return done
}

func TestProviderAddIteratorBlocksUntilStore(t *testing.T) {
	ctx := context.Background()
	h, wm, _, _ := newProvidersHandlerForTest(t)
	h.verify = func(context.Context, string, string, string) error { return nil }

	it, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, wm.lastFloating, "expected a floating prompt to open")

	done := drainDone(t, it)
	select {
	case <-done:
		t.Fatal("iterator completed before the key was submitted")
	case <-time.After(50 * time.Millisecond):
	}

	submitKey(t, wm.lastFloating, "sk-secret")

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("iterator did not complete after the key was stored")
	}

	active, err := h.router.ProviderKeyActive(ctx, llmrouter.ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", active)
}

func TestProviderAddIteratorUnblocksOnContextCancel(t *testing.T) {
	h, wm, _, _ := newProvidersHandlerForTest(t)
	h.verify = func(context.Context, string, string, string) error { return nil }

	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, wm.lastFloating)

	cancelCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := it.Next(cancelCtx); !ok {
				return
			}
		}
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("iterator did not unblock on context cancellation")
	}
}

func TestProviderAddIteratorCloseIsIdempotent(t *testing.T) {
	h, wm, _, _ := newProvidersHandlerForTest(t)
	h.verify = func(context.Context, string, string, string) error { return nil }

	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, wm.lastFloating)

	require.NoError(t, it.Close())
	require.NoError(t, it.Close())

	_, ok := it.Next(context.Background())
	assert.False(t, ok, "Next must report completion after Close")
}

func TestProviderAddVerifyFailOpensConfirm(t *testing.T) {
	ctx := context.Background()
	h, wm, _, prompt := newProvidersHandlerForTest(t)
	prompt.selectIdx = -1 // capture the prompt without auto-selecting
	h.verify = func(context.Context, string, string, string) error {
		return errors.New("401 invalid api key")
	}

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, wm.lastFloating, "redacted key prompt should open")

	submitKey(t, wm.lastFloating, "sk-bad")

	// The confirmation prompt is opened through the shared PromptOpener,
	// not as a hand-rolled floating window.
	require.True(t, prompt.called, "a confirmation prompt should open")
	assert.Equal(t, []string{" yes ", " no "}, prompt.options)
	assert.Contains(t, prompt.message, "401 invalid api key")
	assert.Contains(t, prompt.message, "Do you still want to add it?")

	// Not stored until the user confirms.
	_, err = h.router.ProviderKeyActive(ctx, llmrouter.ProviderOpenAI)
	require.ErrorIs(t, err, llmrouter.ErrAPIKeyNotSet)

	// Select "yes" (option index 0).
	prompt.handler.OnSelect(0, prompt.options[0])

	active, err := h.router.ProviderKeyActive(ctx, llmrouter.ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "sk-bad", active)
}

func TestProviderAddVerifyFailConfirmNo(t *testing.T) {
	ctx := context.Background()
	h, wm, _, prompt := newProvidersHandlerForTest(t)
	prompt.selectIdx = -1
	h.verify = func(context.Context, string, string, string) error {
		return errors.New("401 invalid api key")
	}

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "providers", Args: []string{"openai", "add", "work"}}, nil)
	require.NoError(t, err)
	submitKey(t, wm.lastFloating, "sk-bad")

	require.True(t, prompt.called)
	prompt.handler.OnSelect(1, prompt.options[1]) // "no"

	_, err = h.router.ProviderKeyActive(ctx, llmrouter.ProviderOpenAI)
	require.ErrorIs(t, err, llmrouter.ErrAPIKeyNotSet)
}
