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

package mcp

import (
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeMCPInputSchema(t *testing.T) {
	tests := []struct {
		name   string
		input  map[string]any
		assert func(t *testing.T, out map[string]any)
	}{
		{
			name: "top-level array missing items",
			input: map[string]any{
				"type": "array",
			},
			assert: func(t *testing.T, out map[string]any) {
				items, ok := out["items"].(map[string]any)
				require.True(t, ok, "items must be a map")
				assert.Empty(t, items)
			},
		},
		{
			name: "nested array under properties.foo.properties.bar",
			input: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"foo": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"bar": map[string]any{"type": "array"},
						},
					},
				},
			},
			assert: func(t *testing.T, out map[string]any) {
				bar := out["properties"].(map[string]any)["foo"].(map[string]any)["properties"].(map[string]any)["bar"].(map[string]any)
				_, ok := bar["items"].(map[string]any)
				assert.True(t, ok)
			},
		},
		{
			name: "array already has items - untouched",
			input: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			assert: func(t *testing.T, out map[string]any) {
				items := out["items"].(map[string]any)
				assert.Equal(t, "string", items["type"])
			},
		},
		{
			name: "non-array property without items - untouched",
			input: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
			assert: func(t *testing.T, out map[string]any) {
				name := out["properties"].(map[string]any)["name"].(map[string]any)
				_, has := name["items"]
				assert.False(t, has)
			},
		},
		{
			name: "array under oneOf",
			input: map[string]any{
				"oneOf": []any{
					map[string]any{"type": "array"},
				},
			},
			assert: func(t *testing.T, out map[string]any) {
				arr := out["oneOf"].([]any)[0].(map[string]any)
				_, ok := arr["items"].(map[string]any)
				assert.True(t, ok)
			},
		},
		{
			name: "array under $defs",
			input: map[string]any{
				"$defs": map[string]any{
					"thing": map[string]any{"type": "array"},
				},
			},
			assert: func(t *testing.T, out map[string]any) {
				thing := out["$defs"].(map[string]any)["thing"].(map[string]any)
				_, ok := thing["items"].(map[string]any)
				assert.True(t, ok)
			},
		},
		{
			name: "union array|null type",
			input: map[string]any{
				"type": []any{"array", "null"},
			},
			assert: func(t *testing.T, out map[string]any) {
				_, ok := out["items"].(map[string]any)
				assert.True(t, ok)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeMCPInputSchema("server", "tool", tc.input)
			out, ok := got.(map[string]any)
			require.True(t, ok)
			tc.assert(t, out)
		})
	}
}

func TestSanitizeMCPInputSchema_Nil(t *testing.T) {
	assert.Nil(t, sanitizeMCPInputSchema("server", "tool", nil))
}

func TestSanitizeMCPInputSchema_NonMap(t *testing.T) {
	// Non-map InputSchemas (e.g. json.RawMessage, string) are returned
	// unchanged so the upstream value is preserved.
	in := "not a schema"
	out := sanitizeMCPInputSchema("server", "tool", in)
	assert.Equal(t, in, out)
}

func TestToolAdapterDefinition_SanitizesArrayMissingItems(t *testing.T) {
	mcpTool := &gomcp.Tool{
		Name:        "syntax_query",
		Description: "Query syntax",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"captures": map[string]any{"type": "array"},
			},
		},
	}
	adapter := &toolAdapter{
		serverName: "rune",
		toolName:   mcpTool.Name,
		mcpTool:    mcpTool,
	}

	def := adapter.Definition()
	params, ok := def.Function.Parameters.(map[string]any)
	require.True(t, ok)
	captures, ok := params["properties"].(map[string]any)["captures"].(map[string]any)
	require.True(t, ok)
	items, ok := captures["items"].(map[string]any)
	require.True(t, ok, "items must be injected as a map")
	assert.NotNil(t, items)
}
