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
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

// mcpTestEnv wraps testEnv with an MCP server for
// testing tool calls.
type mcpTestEnv struct {
	*testEnv
	srv *server.MCPServer
}

func newMCPTestEnv(t *testing.T) *mcpTestEnv {
	t.Helper()
	env := newTestEnv(t)

	a := &app{}
	w, err := a.getWorkspace()
	require.NoError(t, err)

	s := server.NewMCPServer(
		"runectl-test", "0.0.1",
		server.WithRecovery(),
	)
	registerMCPTools(context.Background(), s, w)

	return &mcpTestEnv{testEnv: env, srv: s}
}

// callTool sends a tools/call JSON-RPC message and
// returns the result text content.
func (e *mcpTestEnv) callTool(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	resp := e.srv.HandleMessage(context.Background(), data)
	jsonResp, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T: %+v", resp, resp)

	resultJSON, err := json.Marshal(jsonResp.Result)
	require.NoError(t, err)

	var toolResult mcp.CallToolResult
	err = json.Unmarshal(resultJSON, &toolResult)
	require.NoError(t, err)
	require.False(t, toolResult.IsError, "tool returned error: %+v", toolResult.Content)
	require.NotEmpty(t, toolResult.Content)

	text, ok := toolResult.Content[0].(mcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", toolResult.Content[0])
	return text.Text
}

func TestMCPSyntax(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		args  map[string]any
		check func(*testing.T, string)
	}{
		{
			name: "search",
			tool: "syntax_search",
			args: map[string]any{
				"query": "(function_declaration) @fn",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "file:///src/main.go")
				require.Contains(t, out, "func main()")
			},
		},
		{
			name: "search_node",
			tool: "syntax_search_node",
			args: map[string]any{
				"node_types": "func",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "MyFunc")
			},
		},
		{
			name: "query",
			tool: "syntax_query",
			args: map[string]any{
				"uri":   "file:///test.go",
				"query": "(package_clause) @pkg",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "package main")
			},
		},
		{
			name: "query_node",
			tool: "syntax_query_node",
			args: map[string]any{
				"uri":        "file:///test.go",
				"node_types": "namespace",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "main")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newMCPTestEnv(t)
			defer env.cleanup()

			out := env.callTool(t, tt.tool, tt.args)
			tt.check(t, out)
		})
	}
}

// listTools sends a tools/list JSON-RPC message and
// returns the decoded tool definitions keyed by name.
func (e *mcpTestEnv) listTools(t *testing.T) map[string]map[string]any {
	t.Helper()
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	resp := e.srv.HandleMessage(context.Background(), data)
	jsonResp, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T: %+v", resp, resp)

	resultJSON, err := json.Marshal(jsonResp.Result)
	require.NoError(t, err)

	var raw struct {
		Tools []map[string]any `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(resultJSON, &raw))

	out := make(map[string]map[string]any, len(raw.Tools))
	for _, tool := range raw.Tools {
		name, _ := tool["name"].(string)
		out[name] = tool
	}
	return out
}

// TestMCPSyntaxToolSchemas asserts that array properties in the
// syntax_query and syntax_search tool schemas declare an `items`
// schema. Without `items`, OpenAI's strict function-schema validator
// rejects the tool definitions with `invalid_function_parameters`.
func TestMCPSyntaxToolSchemas(t *testing.T) {
	env := newMCPTestEnv(t)
	defer env.cleanup()

	tools := env.listTools(t)

	cases := []struct {
		tool  string
		field string
	}{
		{"syntax_search", "captures"},
		{"syntax_search", "languages"},
		{"syntax_query", "captures"},
	}

	for _, tc := range cases {
		t.Run(tc.tool+"."+tc.field, func(t *testing.T) {
			tool, ok := tools[tc.tool]
			require.True(t, ok, "tool %q not registered", tc.tool)

			schema, ok := tool["inputSchema"].(map[string]any)
			require.True(t, ok, "inputSchema missing for %q", tc.tool)

			props, ok := schema["properties"].(map[string]any)
			require.True(t, ok, "properties missing for %q", tc.tool)

			prop, ok := props[tc.field].(map[string]any)
			require.True(t, ok, "property %q missing for %q", tc.field, tc.tool)
			require.Equal(t, "array", prop["type"], "property %q should be array", tc.field)

			items, ok := prop["items"].(map[string]any)
			require.True(t, ok, "property %q missing items schema for %q", tc.field, tc.tool)
			require.NotEmpty(t, items, "items schema for %q must not be empty", tc.field)
			require.Equal(t, "string", items["type"], "items.type for %q should be string", tc.field)
		})
	}
}

func TestMCPLSP(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		args  map[string]any
		check func(*testing.T, string)
	}{
		{
			name: "documentation",
			tool: "lsp_documentation",
			args: map[string]any{
				"symbol": "MyFunc",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "func main()")
			},
		},
		{
			name: "definition",
			tool: "lsp_definition",
			args: map[string]any{
				"symbol": "MyFunc",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "file:///src/main.go")
				require.Contains(t, out, `"start_line":10`)
			},
		},
		{
			name: "references",
			tool: "lsp_references",
			args: map[string]any{
				"symbol": "MyFunc",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "file:///src/main.go")
				require.Contains(t, out, "file:///src/util.go")
			},
		},
		{
			name: "completion",
			tool: "lsp_completion",
			args: map[string]any{
				"uri":       "file:///src/main.go",
				"line":      0,
				"character": 5,
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "fmt")
				require.Contains(t, out, "Println")
			},
		},
		{
			name: "doc_symbols",
			tool: "lsp_document_symbols",
			args: map[string]any{
				"uri": "file:///src/main.go",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "main")
				require.Contains(t, out, "Function")
			},
		},
		{
			name: "ws_symbols",
			tool: "lsp_workspace_symbols",
			args: map[string]any{
				"query": "My",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "MyFunc")
			},
		},
		{
			name: "diagnostics",
			tool: "lsp_diagnostics",
			args: map[string]any{
				"uri": "file:///src/main.go",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "undefined variable")
				require.Contains(t, out, "Error")
			},
		},
		{
			name: "ws_diags",
			tool: "lsp_workspace_diagnostics",
			args: map[string]any{},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "undefined variable")
				require.Contains(t, out, "unused import")
				require.Contains(t, out, "Error")
				require.Contains(t, out, "Warning")
				require.Contains(t, out, "file:///src/main.go")
				require.Contains(t, out, "file:///src/util.go")
			},
		},
		{
			name: "rename",
			tool: "lsp_rename",
			args: map[string]any{
				"symbol":   "MyFunc",
				"new_name": "newMain",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "file:///src/main.go")
			},
		},
		{
			name: "code_actions",
			tool: "lsp_code_actions",
			args: map[string]any{
				"uri":       "file:///src/main.go",
				"line":      0,
				"character": 0,
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "Extract variable")
				require.Contains(t, out, "Organize imports")
			},
		},
		{
			name: "formatting",
			tool: "lsp_formatting",
			args: map[string]any{
				"uri": "file:///src/main.go",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "package")
			},
		},
		{
			name: "declaration",
			tool: "lsp_declaration",
			args: map[string]any{
				"symbol": "MyFunc",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "file:///src/types.go")
			},
		},
		{
			name: "type_definition",
			tool: "lsp_type_definition",
			args: map[string]any{
				"symbol": "myVar",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "file:///src/types.go")
			},
		},
		{
			name: "implementation",
			tool: "lsp_implementation",
			args: map[string]any{
				"symbol": "MyInterface",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "file:///src/impl1.go")
				require.Contains(t, out, "file:///src/impl2.go")
			},
		},
		{
			name: "prepare_rename",
			tool: "lsp_prepare_rename",
			args: map[string]any{
				"symbol": "MyFunc",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "main")
			},
		},
		{
			name: "sig_help",
			tool: "lsp_signature_help",
			args: map[string]any{
				"uri":       "file:///src/main.go",
				"line":      0,
				"character": 5,
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "Println")
			},
		},
		{
			name: "doc_highlight",
			tool: "lsp_document_highlight",
			args: map[string]any{
				"uri":       "file:///src/main.go",
				"line":      5,
				"character": 5,
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "Read")
				require.Contains(t, out, "Write")
			},
		},
		{
			name: "code_lens",
			tool: "lsp_code_lens",
			args: map[string]any{
				"uri": "file:///src/main.go",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "Run test")
				require.Contains(t, out, "test.run")
			},
		},
		{
			name: "folding_range",
			tool: "lsp_folding_range",
			args: map[string]any{
				"uri": "file:///src/main.go",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "region")
				require.Contains(t, out, "imports")
			},
		},
		{
			name: "prep_call_hier",
			tool: "lsp_prepare_call_hierarchy",
			args: map[string]any{
				"symbol": "MyFunc",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "main")
				require.Contains(t, out, "Function")
			},
		},
		{
			name: "prep_type_hier",
			tool: "lsp_prepare_type_hierarchy",
			args: map[string]any{
				"symbol": "MyInterface",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "Reader")
				require.Contains(t, out, "Interface")
			},
		},
		{
			name: "execute_command",
			tool: "lsp_execute_command",
			args: map[string]any{
				"command": "test.run",
			},
			check: func(t *testing.T, out string) {
				require.Contains(t, out, "command executed: test.run")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newMCPTestEnv(t)
			defer env.cleanup()

			out := env.callTool(t, tt.tool, tt.args)
			if tt.check != nil {
				tt.check(t, out)
			}
		})
	}
}

func TestMCPToolError(t *testing.T) {
	env := newMCPTestEnv(t)
	defer env.cleanup()

	// Call a tool with missing required parameter.
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "lsp_documentation",
			"arguments": map[string]any{},
		},
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	resp := env.srv.HandleMessage(context.Background(), data)
	jsonResp, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T", resp)

	resultJSON, err := json.Marshal(jsonResp.Result)
	require.NoError(t, err)

	var toolResult mcp.CallToolResult
	err = json.Unmarshal(resultJSON, &toolResult)
	require.NoError(t, err)
	require.True(t, toolResult.IsError, "expected tool error for missing params")
}

func TestMCPSymbolNotFound(t *testing.T) {
	env := newMCPTestEnv(t)
	defer env.cleanup()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "lsp_definition",
			"arguments": map[string]any{
				"symbol": "NonExistent",
			},
		},
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	resp := env.srv.HandleMessage(context.Background(), data)
	jsonResp, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "expected JSONRPCResponse, got %T", resp)

	resultJSON, err := json.Marshal(jsonResp.Result)
	require.NoError(t, err)

	var toolResult mcp.CallToolResult
	err = json.Unmarshal(resultJSON, &toolResult)
	require.NoError(t, err)
	require.True(t, toolResult.IsError, "expected error for unknown symbol")

	text, ok := toolResult.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "not found")
}

func TestMCPUnknownTool(t *testing.T) {
	env := newMCPTestEnv(t)
	defer env.cleanup()

	msg := `{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tools/call",
		"params": {
			"name": "nonexistent_tool",
			"arguments": {}
		}
	}`

	resp := env.srv.HandleMessage(context.Background(), []byte(msg))
	// Should return an error response.
	_, isErr := resp.(mcp.JSONRPCError)
	_, isResp := resp.(mcp.JSONRPCResponse)
	require.True(t, isErr || isResp, "expected error or response, got %T", resp)
}
