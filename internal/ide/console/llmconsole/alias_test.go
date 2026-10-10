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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm/openai"
)

func TestAliasSetAndList(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "alias", Args: []string{"set", "default", "openai/" + openai.GPT5Dot5}}, nil)
	require.NoError(t, err)

	stored, err := h.router.Aliases(ctx)
	require.NoError(t, err)
	assert.Equal(t, openai.GPT5Dot5, stored["default"].Name)
	assert.Equal(t, "openai", stored["default"].Provider)
}

func TestAliasBareSetForm(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "alias", Args: []string{"default", "openai/" + openai.GPT5Dot5}}, nil)
	require.NoError(t, err)

	stored, err := h.router.Aliases(ctx)
	require.NoError(t, err)
	assert.Equal(t, openai.GPT5Dot5, stored["default"].Name)
	assert.Equal(t, "openai", stored["default"].Provider)
}

func TestAliasSetRejectsUnknownTarget(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))
	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "alias", Args: []string{"set", "default", "openai/nope"}}, nil)
	require.Error(t, err)
}

func TestAliasRemove(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))
	require.NoError(t, h.router.SetAlias(ctx, "default", "openai/"+openai.GPT5Dot5))

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "alias", Args: []string{"remove", "default"}}, nil)
	require.NoError(t, err)

	stored, err := h.router.Aliases(ctx)
	require.NoError(t, err)
	assert.NotContains(t, stored, "default")
}

func TestAliasRemoveUnsetErrors(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))
	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "alias", Args: []string{"remove", "default"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not set")
}

func TestAliasListShowsReservedNames(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))
	it, err := h.HandleCommand(ctx, repl.Command{Name: "alias", Args: nil}, nil)
	require.NoError(t, err)
	v, ok := it.Next(ctx)
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestAliasCompleteTopLevel(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))
	it, err := h.Complete(ctx, "alias", nil)
	require.NoError(t, err)
	names, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"list", "set", "remove"}, names)
}

func TestAliasCompleteNames(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))
	require.NoError(t, h.router.SetAlias(ctx, "fast", "openai/"+openai.GPT5Dot5))

	it, err := h.Complete(ctx, "alias", []string{"remove", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Contains(t, names, "default")
	assert.Contains(t, names, "query")
	assert.Contains(t, names, "compact")
	assert.Contains(t, names, "dream")
	assert.Contains(t, names, "fast")
}

func TestAliasCompleteSetModelTargets(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))

	it, err := h.Complete(ctx, "alias", []string{"set", "default", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Contains(t, names, "openai/"+openai.GPT5Dot5)
	for _, n := range names {
		assert.Contains(t, n, "/", "target completions must be provider/model qualified")
	}
}

func TestAliasCompleteSetModelTargetsFiltersByPrefix(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))

	it, err := h.Complete(ctx, "alias", []string{"set", "default", "openai/"})
	require.NoError(t, err)
	names, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.NotEmpty(t, names)
	for _, n := range names {
		assert.True(t, strings.HasPrefix(n, "openai/"), "got %q", n)
	}
}

func TestAliasCompleteBareFormModelTargets(t *testing.T) {
	ctx := context.Background()
	h := newAliasHandler(newRouterForTest(t))

	it, err := h.Complete(ctx, "alias", []string{"default", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Contains(t, names, "openai/"+openai.GPT5Dot5)
}

func TestAliasDispatchThroughParentHandler(t *testing.T) {
	ctx := context.Background()
	h := newHandlerForTest(t)

	_, err := h.HandleCommand(ctx,
		repl.Command{Name: "models", Args: []string{"alias", "set", "default", "openai/" + openai.GPT5Dot5}}, nil)
	require.NoError(t, err)

	it, err := h.Complete(ctx, "models", []string{""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Contains(t, names, "alias")
}
