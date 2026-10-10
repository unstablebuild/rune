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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/llm/llamacpp"
)

func TestLocalNilRegistryPanics(t *testing.T) {
	defer func() {
		assert.NotNil(t, recover(), "expected panic for nil local registry")
	}()
	_ = New(Config{Service: newRouterForTest(t), LocalRegistry: nil, Storage: stubStorageForTest(t)})
}

func TestLocalNoArgsUsage(t *testing.T) {
	reg := newTestRegistry(t)
	h := newLocalHandler(reg)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "local", Args: nil}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestLocalUnknownSubcommand(t *testing.T) {
	reg := newTestRegistry(t)
	h := newLocalHandler(reg)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "local", Args: []string{"nope"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown local subcommand")
}

func TestLocalListEmptyCache(t *testing.T) {
	reg := newTestRegistry(t)
	h := newLocalHandler(reg)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "local", Args: []string{"list"}}, nil)
	require.NoError(t, err)
	v, ok := it.Next(context.Background())
	require.True(t, ok)
	require.NotNil(t, v)
}

func TestLocalDeleteUnknownReferenceErrors(t *testing.T) {
	reg := newTestRegistry(t)
	h := newLocalHandler(reg)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "local", Args: []string{"delete", "huggingface.co/foo/bar:latest"}},
		nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not present in the local cache")
}

func TestLocalCompleteTopLevelSubs(t *testing.T) {
	reg := newTestRegistry(t)
	h := newLocalHandler(reg)
	it, err := h.Complete(context.Background(), "local", nil)
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"list", "download", "delete"}, names)
}

func TestLocalCompleteDeleteEmptyCache(t *testing.T) {
	reg := newTestRegistry(t)
	h := newLocalHandler(reg)
	it, err := h.Complete(context.Background(), "local", []string{"delete", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Empty(t, names)
}

// newTestRegistry constructs a llamacpp.Registry rooted in a fresh
// temp directory.
func newTestRegistry(t *testing.T) *llamacpp.Registry {
	t.Helper()
	r, err := llamacpp.NewRegistry(t.TempDir())
	require.NoError(t, err)
	return r
}
