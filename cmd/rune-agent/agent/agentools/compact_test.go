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

package agentools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompact(t *testing.T) {
	tool := newCompact()

	executeTests := []struct {
		name             string
		args             string
		wantInstructions string
	}{
		{name: "no arguments", args: "{}"},
		{name: "empty arguments", args: ""},
		{name: "null instructions", args: `{"instructions":null}`},
		{name: "instructions", args: `{"instructions":"  keep the failing test output\n"}`,
			wantInstructions: "keep the failing test output"},
	}
	for _, tt := range executeTests {
		t.Run("Execute "+tt.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), tt.args)
			assert.Equal(t, "Compacting conversation...", result.Content)
			assert.True(t, result.Compact)
			assert.False(t, result.IsError)
			assert.Equal(t, tt.wantInstructions, result.CompactInstructions)
		})
	}

	t.Run("Execute rejects malformed arguments", func(t *testing.T) {
		result := tool.Execute(context.Background(), "{")
		assert.True(t, result.IsError)
		assert.False(t, result.Compact)
	})

	t.Run("Summary returns empty string", func(t *testing.T) {
		assert.Equal(t, "", tool.Summary("{}"))
	})

	t.Run("Definition takes nullable instructions", func(t *testing.T) {
		def := tool.Definition()
		assert.Equal(t, "compact", def.Function.Name)
		assert.NotEmpty(t, def.Function.Description)
		params, ok := def.Function.Parameters.(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "object", params["type"])
		assert.Equal(t, []string{"instructions"}, params["required"])
		assert.Equal(t, false, params["additionalProperties"])
		props, ok := params["properties"].(map[string]any)
		assert.True(t, ok)
		instructions, ok := props["instructions"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, []string{"string", "null"}, instructions["type"])
	})
}
