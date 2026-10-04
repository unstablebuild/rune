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

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

func newTestActionRouter(lsp semanticapi.LSP, notify *fakeNotifications) textapi.CommandHandler {
	_, h := newRustActionHandler(lsp, &fakeEditor{}, &fakeWM{}, notify, nil,
		lspcmd.NewSelectionTracker(), newFakeExecutor(), newFakeFS(), nil, nil, "/ws", true, true)
	return h
}

func TestRustActionManualListsSubcommands(t *testing.T) {
	manual, _ := newRustActionHandler(&actionLSP{}, &fakeEditor{}, &fakeWM{},
		newFakeNotifications(), nil, lspcmd.NewSelectionTracker(), newFakeExecutor(), newFakeFS(), nil, nil, "/ws", true, true)
	assert.Equal(t, actionCmdName, manual.Name)
	names := make(map[string]bool)
	for _, c := range manual.Commands {
		names[c.Name] = true
	}
	for _, want := range []string{"list", "extract", "inline", "rewrite", "refactor", "quickfix", "organize-imports"} {
		assert.True(t, names[want], "manual missing subcommand %q", want)
	}
}

func TestRustActionExperimentalGating(t *testing.T) {
	experimentalOnly := []string{
		"parent-module", "child-modules", "open-cargo-toml", "external-docs",
		"join-lines", "matching-brace", "on-enter", "move-item-up", "move-item-down",
		"ssr", "runnables", "run", "type", "symbols", "hover", "eval-predicate",
	}
	alwaysOn := []string{"list", "extract", "status", "hir", "expand-macro", "reload-workspace"}

	manualNames := func(experimental bool) map[string]bool {
		manual, h := newRustActionHandler(&actionLSP{}, &fakeEditor{}, &fakeWM{},
			newFakeNotifications(), nil, lspcmd.NewSelectionTracker(),
			newFakeExecutor(), newFakeFS(), nil, nil, "/ws", experimental, false)
		names := make(map[string]bool)
		for _, c := range manual.Commands {
			names[c.Name] = true
		}
		router := h.(*rustActionRouter)
		for name := range router.handlers {
			names[name] = true
		}
		return names
	}

	off := manualNames(false)
	for _, name := range experimentalOnly {
		assert.False(t, off[name], "%q must not register without the experimental flag", name)
	}
	for _, name := range alwaysOn {
		assert.True(t, off[name], "%q must register regardless of the experimental flag", name)
	}

	on := manualNames(true)
	for _, name := range append(append([]string{}, experimentalOnly...), alwaysOn...) {
		assert.True(t, on[name], "%q must register with the experimental flag", name)
	}
}

func TestRustActionMemoryUsageGating(t *testing.T) {
	registered := func(memoryUsage bool) (manual, handler bool) {
		m, h := newRustActionHandler(&actionLSP{}, &fakeEditor{}, &fakeWM{},
			newFakeNotifications(), nil, lspcmd.NewSelectionTracker(),
			newFakeExecutor(), newFakeFS(), nil, nil, "/ws", true, memoryUsage)
		for _, c := range m.Commands {
			if c.Name == "memory-usage" {
				manual = true
			}
		}
		_, handler = h.(*rustActionRouter).handlers["memory-usage"]
		return manual, handler
	}

	offManual, offHandler := registered(false)
	assert.False(t, offManual, "memory-usage must not be in the manual without the flag")
	assert.False(t, offHandler, "memory-usage must not register without the flag")

	onManual, onHandler := registered(true)
	assert.True(t, onManual, "memory-usage must be in the manual with the flag")
	assert.True(t, onHandler, "memory-usage must register with the flag")
}

func TestRustActionRouterUnknownCommand(t *testing.T) {
	h := newTestActionRouter(&actionLSP{}, newFakeNotifications())
	err := h.HandleCommand(context.Background(), textapi.Command{Name: "notrust"})
	require.Error(t, err)
}

func TestRustActionRouterMissingSubcommand(t *testing.T) {
	h := newTestActionRouter(&actionLSP{}, newFakeNotifications())
	err := h.HandleCommand(context.Background(), textapi.Command{Name: actionCmdName})
	require.Error(t, err)
}

func TestRustActionRouterUnknownSubcommand(t *testing.T) {
	h := newTestActionRouter(&actionLSP{}, newFakeNotifications())
	err := h.HandleCommand(context.Background(),
		textapi.Command{Name: actionCmdName, Args: []string{"nope"}})
	require.ErrorContains(t, err, "unknown rust subcommand")
}

func TestRustActionRouterDispatchesToKind(t *testing.T) {
	uri := newTestURI(t)
	lsp := &actionLSP{}
	h := newTestActionRouter(lsp, newFakeNotifications())
	cmd := cmdFor(uri, actionCmdName, []string{"extract"})
	require.NoError(t, h.HandleCommand(context.Background(), cmd))
	require.Equal(t, []semanticapi.CodeActionKind{"refactor.extract"}, lsp.params.Context.Only)
}

func TestRustActionRouterCompletesSubcommands(t *testing.T) {
	h := newTestActionRouter(&actionLSP{}, newFakeNotifications())
	it, err := h.Complete(context.Background(), actionCmdName, []string{"ex"})
	require.NoError(t, err)
	got, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Contains(t, got, "extract")
	// Completion of the subcommand list is sorted.
	for i := 1; i < len(got); i++ {
		assert.LessOrEqual(t, got[i-1], got[i])
	}
}

func TestRustActionRouterRefreshesZeroCursor(t *testing.T) {
	live := term.Coordinates{X: 4, Y: 270}
	cases := []struct {
		name     string
		cursor   term.Coordinates
		resource bool
		want     term.Coordinates
	}{
		{name: "zero snapshot uses live cursor", resource: true, want: live},
		{name: "captured snapshot wins", cursor: term.Coordinates{X: 2, Y: 7}, resource: true, want: term.Coordinates{X: 2, Y: 7}},
		{name: "no resource stays zero", want: term.Coordinates{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			me := newMockEditor()
			var got textapi.Command
			router := &rustActionRouter{
				editor: me,
				handlers: map[string]textapi.CommandHandler{
					"probe": textapi.FuncCommandHandler(
						func(_ context.Context, cmd textapi.Command) error {
							got = cmd
							return nil
						}, nil),
				},
			}
			cmd := textapi.Command{Name: actionCmdName, Args: []string{"probe"}}
			cmd.Cursor.Content = tc.cursor
			if tc.resource {
				resource := &stubResource{uri: newTestURI(t)}
				me.Register(resource)
				require.NoError(t, me.SetCursor(resource, live))
				cmd.Resource = resource
			}
			require.NoError(t, router.HandleCommand(context.Background(), cmd))
			assert.Equal(t, tc.want, got.Cursor.Content)
		})
	}
}
