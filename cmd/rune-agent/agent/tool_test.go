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

package agent

import (
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryWithFilteredTools(t *testing.T) {
	t.Run("filters to allowed names", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "read_file"},
			&mockTool{name: "search"},
			&mockTool{name: "bash"},
		)

		filtered := r.WithFilteredTools([]string{"read_file", "search"})

		_, ok := filtered.Get("read_file", "")
		assert.True(t, ok, "read_file should be in filtered registry")
		_, ok = filtered.Get("search", "")
		assert.True(t, ok, "search should be in filtered registry")
		_, ok = filtered.Get("bash", "")
		assert.False(t, ok, "bash should not be in filtered registry")
	})

	t.Run("unknown names are silently ignored", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "read_file"},
		)

		filtered := r.WithFilteredTools([]string{"read_file", "nonexistent"})

		assert.Len(t, filtered.AllTools(), 1)
		_, ok := filtered.Get("read_file", "")
		assert.True(t, ok)
	})

	t.Run("empty allowed names returns empty registry", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "read_file"},
		)

		filtered := r.WithFilteredTools(nil)

		assert.Empty(t, filtered.AllTools())
	})

	t.Run("preserves overrides for allowed names", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "search"},
			&mockTool{name: "bash"},
		)
		override := &mockTool{name: "search", result: ToolResult{Content: "openai-search"}}
		r.RegisterOverrides("openai", override)

		filtered := r.WithFilteredTools([]string{"search"})

		got, ok := filtered.Get("search", "openai")
		assert.True(t, ok)
		assert.Equal(t, override, got)
	})

	t.Run("does not propagate exclusions for allowed names", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "exit_plan_mode"},
			&mockTool{name: "bash"},
		)
		r.RegisterExclusions("openai", "exit_plan_mode")

		filtered := r.WithFilteredTools([]string{"exit_plan_mode"})

		// exit_plan_mode is explicitly allowed, so the exclusion should not carry over.
		got, ok := filtered.Get("exit_plan_mode", "openai")
		assert.True(t, ok, "explicitly allowed tool should not be excluded")
		assert.NotNil(t, got)

		// Tools() should include it.
		tools := filtered.Tools("openai")
		var names []string
		for _, tool := range tools {
			names = append(names, tool.Function.Name)
		}
		assert.Contains(t, names, "exit_plan_mode")
	})

	t.Run("drops overrides for excluded names", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "search"},
			&mockTool{name: "bash"},
		)
		r.RegisterOverrides("openai", &mockTool{name: "bash"})

		filtered := r.WithFilteredTools([]string{"search"})

		_, ok := filtered.Get("bash", "openai")
		assert.False(t, ok)
	})
}

func TestRegistryOverrides(t *testing.T) {
	t.Run("override replaces base tool for provider", func(t *testing.T) {
		base := &mockTool{name: "search", result: ToolResult{Content: "base"}}
		override := &mockTool{name: "search", result: ToolResult{Content: "openai"}}
		r := NewRegistry(base)
		r.RegisterOverrides("openai", override)

		got, ok := r.Get("search", "openai")
		require.True(t, ok)
		assert.Equal(t, override, got)

		got, ok = r.Get("search", "")
		require.True(t, ok)
		assert.Equal(t, base, got)

		got, ok = r.Get("search", "anthropic")
		require.True(t, ok)
		assert.Equal(t, base, got)
	})

	t.Run("override adds provider-only tool", func(t *testing.T) {
		r := NewRegistry(&mockTool{name: "read_file"})
		extra := &mockTool{name: "extra_tool"}
		r.RegisterOverrides("openai", extra)

		_, ok := r.Get("extra_tool", "")
		assert.False(t, ok, "extra_tool should not exist for empty provider")

		got, ok := r.Get("extra_tool", "openai")
		assert.True(t, ok)
		assert.Equal(t, extra, got)
	})

	t.Run("exclude removes base tool for provider", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "search"},
			&mockTool{name: "bash"},
		)
		r.RegisterExclusions("gemini", "bash")

		_, ok := r.Get("bash", "")
		assert.True(t, ok, "bash should exist for empty provider")

		_, ok = r.Get("bash", "gemini")
		assert.False(t, ok, "bash should be excluded for gemini")

		_, ok = r.Get("search", "gemini")
		assert.True(t, ok, "search should still exist for gemini")
	})

	t.Run("Tools returns merged definitions for provider", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "a"},
			&mockTool{name: "b"},
		)
		r.RegisterOverrides("openai", &mockTool{name: "c"})
		r.RegisterExclusions("openai", "b")

		tools := r.Tools("openai")
		names := make([]string, len(tools))
		for i, tool := range tools {
			names[i] = tool.Function.Name
		}
		sort.Strings(names)
		assert.Equal(t, []string{"a", "c"}, names)
	})

}

func TestRegistryAddOverrides(t *testing.T) {
	t.Run("merges overrides and exclusions", func(t *testing.T) {
		src := NewRegistry(&mockTool{name: "search"})
		override := &mockTool{name: "search", result: ToolResult{Content: "openai-search"}}
		src.RegisterOverrides("openai", override)
		src.RegisterExclusions("openai", "bash")

		dst := NewRegistry(
			&mockTool{name: "search"},
			&mockTool{name: "bash"},
		)
		dst.AddOverrides(src.Overrides())

		got, ok := dst.Get("search", "openai")
		require.True(t, ok)
		assert.Equal(t, override, got)

		_, ok = dst.Get("bash", "openai")
		assert.False(t, ok, "bash should be excluded for openai")
	})

	t.Run("merges with existing overrides", func(t *testing.T) {
		src := NewRegistry()
		src.RegisterOverrides("openai", &mockTool{name: "b"})

		dst := NewRegistry(&mockTool{name: "a"})
		existing := &mockTool{name: "a", result: ToolResult{Content: "openai-a"}}
		dst.RegisterOverrides("openai", existing)
		dst.AddOverrides(src.Overrides())

		got, ok := dst.Get("a", "openai")
		require.True(t, ok)
		assert.Equal(t, existing, got, "existing override should be preserved")

		got, ok = dst.Get("b", "openai")
		require.True(t, ok)
		assert.Equal(t, "b", got.Definition().Function.Name, "copied override should be present")
	})
}

func TestRegistryRegisterReplacement(t *testing.T) {
	t.Run("excludes base, adds replacement, records pairing", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "bash"},
			&mockTool{name: "read_file"},
		)
		r.RegisterReplacement("openai", "bash", &mockTool{name: "exec_command"})

		_, ok := r.Get("bash", "openai")
		assert.False(t, ok, "bash should be excluded for openai")

		got, ok := r.Get("exec_command", "openai")
		require.True(t, ok, "exec_command should be present for openai")
		assert.Equal(t, "exec_command", got.Definition().Function.Name)

		assert.Equal(t, "exec_command", r.ReplacementFor("bash", "openai"))
	})

	t.Run("ReplacementFor is scoped per provider", func(t *testing.T) {
		r := NewRegistry(&mockTool{name: "bash"})
		r.RegisterReplacement("openai", "bash", &mockTool{name: "exec_command"})

		assert.Equal(t, "exec_command", r.ReplacementFor("bash", "openai"))
		assert.Empty(t, r.ReplacementFor("bash", "codex"))
		assert.Empty(t, r.ReplacementFor("bash", ""))
		assert.Empty(t, r.ReplacementFor("search_content", "openai"))
	})

	t.Run("replacements survive AddOverrides", func(t *testing.T) {
		src := NewRegistry(&mockTool{name: "bash"})
		src.RegisterReplacement("openai", "bash", &mockTool{name: "exec_command"})

		dst := NewRegistry(&mockTool{name: "bash"})
		dst.AddOverrides(src.Overrides())

		_, ok := dst.Get("bash", "openai")
		assert.False(t, ok, "bash should be excluded after copy")
		_, ok = dst.Get("exec_command", "openai")
		assert.True(t, ok, "exec_command should be present after copy")
		assert.Equal(t, "exec_command", dst.ReplacementFor("bash", "openai"))
	})

	t.Run("WithFilteredTools drops replacements whose target is filtered out", func(t *testing.T) {
		r := NewRegistry(
			&mockTool{name: "bash"},
			&mockTool{name: "exec_command"},
			&mockTool{name: "read_file"},
		)
		r.RegisterReplacement("openai", "bash", &mockTool{name: "exec_command"})

		// exec_command not in allowed set -> replacement dropped.
		filtered := r.WithFilteredTools([]string{"read_file"})
		assert.Empty(t, filtered.ReplacementFor("bash", "openai"))

		// exec_command allowed -> replacement preserved.
		kept := r.WithFilteredTools([]string{"read_file", "exec_command"})
		assert.Equal(t, "exec_command", kept.ReplacementFor("bash", "openai"))
	})
}

func TestRegistryAddConcurrentWithReads(t *testing.T) {
	t.Parallel()

	r := NewRegistry(&mockTool{name: "base"})

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		name := fmt.Sprintf("late-%d", i)
		go func() {
			defer wg.Done()
			r.Add(&mockTool{name: name})
		}()
		go func() {
			defer wg.Done()
			_ = r.Tools("")
			_, _ = r.Get("base", "")
		}()
	}
	wg.Wait()

	assert.Len(t, r.Tools(""), 9)
	for i := range 8 {
		_, ok := r.Get(fmt.Sprintf("late-%d", i), "")
		assert.True(t, ok)
	}
}
