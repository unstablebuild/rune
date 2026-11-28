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

// The tests in this file run `runectl mcp` as a child process and drive it over its stdin
// and stdout with an MCP client, as Claude Code does. The child serves the test host from
// newTestEnv, so every tool call crosses both the MCP stdio transport and the host's gRPC
// API.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/cli/mcptools"
	"google.golang.org/protobuf/encoding/protojson"
	"unstable.build/rune/internal/debug"
)

const mcpHelperEnv = "RUNECTL_MCP_HELPER"

// TestMCPProcessHelper is the child process: it runs `runectl mcp` when started by
// startMCPProcess.
func TestMCPProcessHelper(t *testing.T) {
	if os.Getenv(mcpHelperEnv) != "1" {
		return
	}
	os.Args = []string{"runectl", "mcp"}
	main()
	os.Exit(0)
}

// startMCPProcess starts `runectl mcp` against a new test host and returns the host and
// an initialized client of the process over its stdin and stdout. Cleanup closes the
// process's stdin and requires it to exit cleanly.
func startMCPProcess(t *testing.T) (*testEnv, *client.Client) {
	t.Helper()
	env := newTestEnv(t)
	t.Cleanup(env.cleanup)

	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^TestMCPProcessHelper$")
	cmd.Env = append(os.Environ(), mcpHelperEnv+"=1")
	// Wait collects all of stderr, including what the process prints as it exits.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)

	c := client.NewClient(transport.NewIO(stdout, stdin, nil))
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() {
		// Closing the client closes the process's stdin.
		require.NoError(t, c.Close())
		go debug.CapturePanicReport(func() { exited <- cmd.Wait() })
		select {
		case err := <-exited:
			require.NoError(t, err,
				"runectl mcp did not exit cleanly when its stdin closed; stderr:\n%s",
				stderr.String())
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Fatalf("runectl mcp did not exit when its stdin closed; stderr:\n%s",
				stderr.String())
		}
	})

	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "runectl-test", Version: "0.0.1"}
	res, err := c.Initialize(mcpCtx(t), req)
	require.NoError(t, err)
	require.Equal(t, "runectl", res.ServerInfo.Name)
	require.Equal(t, "0.1.0", res.ServerInfo.Version)
	require.Equal(t, mcp.LATEST_PROTOCOL_VERSION, res.ProtocolVersion)
	require.NotNil(t, res.Capabilities.Tools)
	env.rpcs.take()
	return env, c
}

func mcpCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func processListTools(t *testing.T, c *client.Client) map[string]mcp.Tool {
	t.Helper()
	res, err := c.ListTools(mcpCtx(t), mcp.ListToolsRequest{})
	require.NoError(t, err)
	require.Empty(t, res.NextCursor, "every tool on one page")
	out := make(map[string]mcp.Tool, len(res.Tools))
	for _, tl := range res.Tools {
		require.NotContains(t, out, tl.Name, "tool listed twice")
		out[tl.Name] = tl
	}
	return out
}

func processCall(
	t *testing.T,
	c *client.Client,
	name string,
	args map[string]any,
) (*mcp.CallToolResult, error) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	return c.CallTool(mcpCtx(t), req)
}

// processText returns the text of a result's single text block.
// processText returns the JSON text block a tool answers first.
func processText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok, "content is %T", res.Content[0])
	return text.Text
}

// processHint returns the hint block a symbol tool answers after its JSON, or "".
func processHint(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.LessOrEqual(t, len(res.Content), 2)
	if len(res.Content) < 2 {
		return ""
	}
	text, ok := res.Content[1].(mcp.TextContent)
	require.True(t, ok, "content is %T", res.Content[1])
	return text.Text
}

// isBareSymbolCall reports whether args name a symbol without a qualifier, which the
// symbol tools answer with a hint to qualify it.
func isBareSymbolCall(args map[string]any) bool {
	sym, ok := args["symbol"].(string)
	return ok && !strings.Contains(sym, ".")
}

// mcpTools are every tool runectl mcp serves, sorted.
var mcpTools = []string{
	"lsp_code_actions",
	"lsp_code_lens",
	"lsp_completion",
	"lsp_declaration",
	"lsp_definition",
	"lsp_diagnostics",
	"lsp_document_highlight",
	"lsp_document_symbols",
	"lsp_documentation",
	"lsp_execute_command",
	"lsp_folding_range",
	"lsp_formatting",
	"lsp_implementation",
	"lsp_incoming_calls",
	"lsp_on_type_formatting",
	"lsp_outgoing_calls",
	"lsp_prepare_call_hierarchy",
	"lsp_prepare_rename",
	"lsp_prepare_type_hierarchy",
	"lsp_range_formatting",
	"lsp_references",
	"lsp_rename",
	"lsp_selection_range",
	"lsp_signature_help",
	"lsp_type_definition",
	"lsp_type_subtypes",
	"lsp_type_supertypes",
	"lsp_workspace_diagnostics",
	"lsp_workspace_symbols",
	"regexp_search",
	"semantic_search",
	"syntax_query",
	"syntax_query_node",
	"syntax_search",
	"syntax_search_node",
}

func TestMCPProcessListTools(t *testing.T) {
	_, c := startMCPProcess(t)
	tools := processListTools(t, c)
	require.Equal(t, mcpTools, slices.Sorted(maps.Keys(tools)))

	var alwaysLoad []string
	for _, name := range mcpTools {
		tl := tools[name]
		require.NotEmpty(t, tl.Description, name)
		require.Equal(t, "object", tl.InputSchema.Type, name)
		for _, arg := range tl.InputSchema.Required {
			require.Contains(t, tl.InputSchema.Properties, arg,
				"%s requires undeclared argument %q", name, arg)
		}
		if tl.Meta != nil {
			require.Equal(t, map[string]any{mcptools.AlwaysLoadMetaKey: true},
				tl.Meta.AdditionalFields, name)
			alwaysLoad = append(alwaysLoad, name)
		}
	}
	require.Equal(t, []string{
		"lsp_definition", "lsp_diagnostics", "lsp_document_symbols",
		"lsp_implementation", "lsp_references", "lsp_workspace_symbols",
	}, alwaysLoad)
}

// processCase is a call of one tool against the test host, the host RPCs it makes, and
// its exact result.
type processCase struct {
	tool string
	args map[string]any
	// rpcs are the host RPCs the call makes, in order.
	rpcs []string
	// req is the protojson of the last RPC's request.
	req  string
	want string
}

const (
	srcMain  = "file:///src/main.go"
	srcTypes = "file:///src/types.go"
)

func atPos(uri string, line, char int) map[string]any {
	return map[string]any{"uri": uri, "line": line, "character": char}
}

func hierItem(name, kind, uri string, sl, sc, el, ec int) map[string]any {
	return map[string]any{
		"name": name, "kind": kind, "uri": uri,
		"start_line": sl, "start_character": sc,
		"end_line": el, "end_character": ec,
	}
}

// processCases returns a case for every tool, sorted by tool. The results are the mockLSP
// and mockSyntax answers as the tools format them, and the requests are what the tools
// ask the host for the arguments given.
func processCases() []processCase {
	const (
		// The symbol cases name symbols without a dot, which the symbol index refuses, so
		// the tools then search the workspace's symbols.
		index   = "Syntax/ResolveSymbol"
		resolve = "LSP/WorkspaceSymbol"
		doc     = `{"textDocument":{"uri":"file:///src/main.go"}}`
		at37    = `{"textDocument":{"uri":"file:///src/main.go"},` +
			`"position":{"line":3,"character":7}}`
		// at MyFunc, which the tools resolve to main.go 5:0.
		atMyFunc = `{"textDocument":{"uri":"file:///src/main.go"},"position":{"line":5}}`
		// at MyInterface, which resolves to types.go 15:5.
		atMyInterface = `{"textDocument":{"uri":"file:///src/types.go"},` +
			`"position":{"line":15,"character":5}}`
	)
	return []processCase{
		{
			tool: "lsp_code_actions", args: atPos(srcMain, 3, 7),
			rpcs: []string{"LSP/CodeAction"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},` +
				`"range":{"start":{"line":3,"character":7},"end":{"line":3,"character":7}}}`,
			want: `[{"title":"Extract variable","kind":"refactor.extract"},` +
				`{"title":"Organize imports","kind":"source.organizeImports"},` +
				`{"title":"Run test","kind":"command"}]`,
		},
		{
			tool: "lsp_code_lens", args: map[string]any{"uri": srcMain},
			rpcs: []string{"LSP/CodeLens"}, req: doc,
			want: `[{"start_line":5,"start_char":0,"end_line":5,"end_char":10,` +
				`"title":"Run test","command":"test.run"}]`,
		},
		{
			tool: "lsp_completion", args: atPos(srcMain, 3, 7),
			rpcs: []string{"LSP/Completion"}, req: at37,
			want: `[{"label":"fmt","kind":"Module","detail":"package fmt"},` +
				`{"label":"Println","kind":"Function","detail":"func Println(a ...any)"}]`,
		},
		{
			tool: "lsp_declaration", args: map[string]any{"symbol": "MyFunc"},
			rpcs: []string{index, resolve, "LSP/Declaration"}, req: atMyFunc,
			want: `[{"uri":"file:///src/types.go","start_line":15,"start_char":5,` +
				`"end_line":15,"end_char":15}]`,
		},
		{
			tool: "lsp_definition", args: map[string]any{"symbol": "MyFunc"},
			rpcs: []string{index, resolve, "LSP/Definition"}, req: atMyFunc,
			want: `[{"uri":"file:///src/main.go","start_line":10,"start_char":5,` +
				`"end_line":10,"end_char":15}]`,
		},
		{
			tool: "lsp_diagnostics", args: map[string]any{"uri": srcMain},
			rpcs: []string{"LSP/Diagnostic"}, req: doc,
			want: `[{"severity":"Error","start_line":3,"start_char":10,"end_line":3,` +
				`"end_char":15,` +
				`"message":"undefined variable","source":"test","code":"E001"}]`,
		},
		{
			tool: "lsp_document_highlight", args: atPos(srcMain, 3, 7),
			rpcs: []string{"LSP/DocumentHighlight"}, req: at37,
			want: `[{"kind":"Read","start_line":5,"start_char":5,"end_line":5,` +
				`"end_char":9},` +
				`{"kind":"Write","start_line":10,"start_char":3,"end_line":10,"end_char":7}]`,
		},
		{
			tool: "lsp_document_symbols", args: map[string]any{"uri": srcMain},
			rpcs: []string{"LSP/DocumentSymbol"}, req: doc,
			want: `[{"name":"main","detail":"","kind":"Function","depth":0,` +
				`"start_line":5,"start_char":0,"end_line":10,"end_char":1},` +
				`{"name":"x","detail":"","kind":"Variable","depth":1,` +
				`"start_line":6,"start_char":1,"end_line":6,"end_char":10}]`,
		},
		{
			tool: "lsp_documentation", args: map[string]any{"symbol": "MyFunc"},
			rpcs: []string{index, resolve, "LSP/Hover"}, req: atMyFunc,
			want: `[{"uri":"file:///src/main.go","start_line":5,"start_char":0,` +
				`"contents":"func main()","kind":"plaintext"}]`,
		},
		{
			tool: "lsp_execute_command", args: map[string]any{"command": "test.run"},
			rpcs: []string{"LSP/ExecuteCommand"}, req: `{"command":"test.run"}`,
			want: `{"result":"command executed: test.run"}`,
		},
		{
			tool: "lsp_folding_range", args: map[string]any{"uri": srcMain},
			rpcs: []string{"LSP/FoldingRange"}, req: doc,
			want: `[{"start_line":5,"start_char":0,"end_line":20,"end_char":1,` +
				`"kind":"region"},` +
				`{"start_line":1,"start_char":0,"end_line":3,"end_char":0,"kind":"imports"}]`,
		},
		{
			tool: "lsp_formatting",
			args: map[string]any{"uri": srcMain, "tab_size": 2, "insert_spaces": true},
			rpcs: []string{"LSP/Formatting"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},` +
				`"tabSize":2,"insertSpaces":true}`,
			want: `[{"start_line":0,"start_char":0,"end_line":0,"end_char":5,` +
				`"new_text":"package"},` +
				`{"start_line":5,"start_char":0,"end_line":5,"end_char":2,"new_text":"\t"}]`,
		},
		{
			tool: "lsp_implementation", args: map[string]any{"symbol": "MyInterface"},
			rpcs: []string{index, resolve, "LSP/Implementation"}, req: atMyInterface,
			want: `[{"uri":"file:///src/impl1.go","start_line":10,"start_char":0,` +
				`"end_line":10,"end_char":10},` +
				`{"uri":"file:///src/impl2.go","start_line":25,"start_char":0,` +
				`"end_line":25,"end_char":10}]`,
		},
		{
			tool: "lsp_incoming_calls",
			args: hierItem("MyFunc", "Function", srcMain, 5, 0, 10, 1),
			rpcs: []string{"LSP/CallHierarchyIncomingCalls"},
			req: `{"item":{"name":"MyFunc","kind":12,"uri":"file:///src/main.go",` +
				`"range":{"start":{"line":5},"end":{"line":10,"character":1}},` +
				`"selectionRange":{"start":{"line":5},"end":{"line":10,"character":1}}}}`,
			want: `[{"from_name":"runApp","from_kind":"Function",` +
				`"from_uri":"file:///src/app.go",` +
				`"from_start_line":20,"from_start_char":0}]`,
		},
		{
			tool: "lsp_on_type_formatting",
			args: map[string]any{"uri": srcMain, "line": 3, "character": 7, "ch": "}"},
			rpcs: []string{"LSP/OnTypeFormatting"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},` +
				`"position":{"line":3,"character":7},` +
				`"character":"}","tabSize":4,"insertSpaces":true}`,
			want: `[{"start_line":5,"start_char":0,"end_line":5,"end_char":2,` +
				`"new_text":"\t"}]`,
		},
		{
			tool: "lsp_outgoing_calls",
			args: hierItem("MyFunc", "Function", srcMain, 5, 0, 10, 1),
			rpcs: []string{"LSP/CallHierarchyOutgoingCalls"},
			req: `{"item":{"name":"MyFunc","kind":12,"uri":"file:///src/main.go",` +
				`"range":{"start":{"line":5},"end":{"line":10,"character":1}},` +
				`"selectionRange":{"start":{"line":5},"end":{"line":10,"character":1}}}}`,
			want: `[{"to_name":"fmt.Println","to_kind":"Function",` +
				`"to_uri":"file:///go/src/fmt/print.go",` +
				`"to_start_line":100,"to_start_char":0}]`,
		},
		{
			tool: "lsp_prepare_call_hierarchy", args: map[string]any{"symbol": "MyFunc"},
			rpcs: []string{index, resolve, "LSP/PrepareCallHierarchy"}, req: atMyFunc,
			want: `[{"name":"main","kind":"Function","uri":"file:///src/main.go",` +
				`"start_line":5,"start_char":0,"end_line":10,"end_char":1}]`,
		},
		{
			tool: "lsp_prepare_rename", args: map[string]any{"symbol": "MyFunc"},
			rpcs: []string{index, resolve, "LSP/PrepareRename"}, req: atMyFunc,
			want: `{"start_line":5,"start_char":5,"end_line":5,"end_char":9,` +
				`"placeholder":"main"}`,
		},
		{
			tool: "lsp_prepare_type_hierarchy", args: map[string]any{
				"symbol": "MyInterface"},
			rpcs: []string{index, resolve, "LSP/PrepareTypeHierarchy"}, req: atMyInterface,
			want: `[{"name":"Reader","kind":"Interface","uri":"file:///src/io.go",` +
				`"detail":"interface",` +
				`"start_line":15,"start_char":0,"end_line":18,"end_char":1}]`,
		},
		{
			tool: "lsp_range_formatting",
			args: map[string]any{
				"uri": srcMain, "start_line": 2, "start_character": 1,
				"end_line": 4, "end_character": 0,
			},
			rpcs: []string{"LSP/RangeFormatting"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},` +
				`"range":{"start":{"line":2,"character":1},"end":{"line":4}},` +
				`"tabSize":4,"insertSpaces":true}`,
			want: `[{"start_line":0,"start_char":0,"end_line":0,"end_char":5,` +
				`"new_text":"fixed"}]`,
		},
		{
			tool: "lsp_references", args: map[string]any{"symbol": "MyFunc",
				"include_declaration": true},
			rpcs: []string{index, resolve, "LSP/References"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},"position":{"line":5},` +
				`"includeDeclaration":true}`,
			want: `[{"uri":"file:///src/main.go","start_line":10,"start_char":5,` +
				`"end_line":10,"end_char":15},` +
				`{"uri":"file:///src/util.go","start_line":20,"start_char":3,` +
				`"end_line":20,"end_char":13}]`,
		},
		{
			tool: "lsp_rename", args: map[string]any{"symbol": "MyFunc",
				"new_name": "Renamed"},
			rpcs: []string{index, resolve, "LSP/Rename"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},"position":{"line":5},` +
				`"newName":"Renamed"}`,
			want: `{"file:///src/main.go":2}`,
		},
		{
			tool: "lsp_selection_range", args: atPos(srcMain, 3, 7),
			rpcs: []string{"LSP/SelectionRange"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},` +
				`"positions":[{"line":3,"character":7}]}`,
			want: `[{"start_line":5,"start_char":5,"end_line":5,"end_char":9,"depth":0},` +
				`{"start_line":5,"start_char":0,"end_line":10,"end_char":1,"depth":1}]`,
		},
		{
			tool: "lsp_signature_help", args: atPos(srcMain, 3, 7),
			rpcs: []string{"LSP/SignatureHelp"}, req: at37,
			want: `{"label":"func Println(a ...any) (n int, err error)",` +
				`"active_signature":0,"active_parameter":0,"parameters":"a ...any"}`,
		},
		{
			tool: "lsp_type_definition", args: map[string]any{"symbol": "myVar"},
			rpcs: []string{index, resolve, "LSP/TypeDefinition"},
			req: `{"textDocument":{"uri":"file:///src/main.go"},` +
				`"position":{"line":3,"character":4}}`,
			want: `[{"uri":"file:///src/types.go","start_line":20,"start_char":5,` +
				`"end_line":20,"end_char":12}]`,
		},
		{
			tool: "lsp_type_subtypes", args: hierItem("MyInterface", "Interface",
				srcTypes, 15, 5, 18, 1),
			rpcs: []string{"LSP/TypeHierarchySubtypes"},
			req: `{"item":{"name":"MyInterface","kind":11,"uri":"file:///src/types.go",` +
				`"range":{"start":{"line":15,"character":5},` +
				`"end":{"line":18,"character":1}},` +
				`"selectionRange":{"start":{"line":15,"character":5},` +
				`"end":{"line":18,"character":1}}}}`,
			want: `[{"name":"MyReader","kind":"Struct","uri":"file:///src/reader.go",` +
				`"detail":"struct",` +
				`"start_line":10,"start_char":0,"end_line":15,"end_char":1},` +
				`{"name":"BufferedReader","kind":"Struct",` +
				`"uri":"file:///src/buffered.go","detail":"struct",` +
				`"start_line":8,"start_char":0,"end_line":12,"end_char":1}]`,
		},
		{
			tool: "lsp_type_supertypes", args: hierItem("MyStruct", "Struct", srcTypes,
				20, 5, 25, 1),
			rpcs: []string{"LSP/TypeHierarchySupertypes"},
			req: `{"item":{"name":"MyStruct","kind":23,"uri":"file:///src/types.go",` +
				`"range":{"start":{"line":20,"character":5},` +
				`"end":{"line":25,"character":1}},` +
				`"selectionRange":{"start":{"line":20,"character":5},` +
				`"end":{"line":25,"character":1}}}}`,
			want: `[{"name":"io.Reader","kind":"Interface",` +
				`"uri":"file:///go/src/io/io.go","detail":"",` +
				`"start_line":50,"start_char":0,"end_line":53,"end_char":1}]`,
		},
		{
			tool: "lsp_workspace_diagnostics", args: map[string]any{},
			rpcs: []string{"LSP/WorkspaceDiagnostic"}, req: `{}`,
			want: `[{"uri":"file:///src/main.go","severity":"Error","start_line":3,` +
				`"start_char":10,` +
				`"end_line":3,"end_char":15,"message":"undefined variable",` +
				`"source":"test","code":"E001"},` +
				`{"uri":"file:///src/util.go","severity":"Warning","start_line":7,` +
				`"start_char":0,` +
				`"end_line":7,"end_char":5,"message":"unused import","source":"test",` +
				`"code":"W002"}]`,
		},
		{
			tool: "lsp_workspace_symbols", args: map[string]any{"query": "MyStruct"},
			rpcs: []string{"LSP/WorkspaceSymbol"}, req: `{"query":"MyStruct"}`,
			want: `[{"name":"MyStruct","kind":"Struct","uri":"file:///src/types.go",` +
				`"start_line":20,"start_char":5},` +
				`{"name":"MyStruct.MyMethod","kind":"Method",` +
				`"uri":"file:///src/types.go","start_line":30,"start_char":0}]`,
		},
		{
			tool: "regexp_search",
			args: map[string]any{"pattern": "invoice", "roots": []any{"billing"},
				"include": []any{"*.go"}},
			rpcs: []string{"Searcher/RegexpSearch"},
			req:  `{"pattern":"invoice","roots":["billing"],"include":["*.go"],"limit":201}`,
			want: `[{"path":"billing/invoice.go","line":42,"text":"\treturn invoice(invoice)"},` +
				`{"path":".github/ci.yml","line":3,"text":"runs-on: invoice"}]`,
		},
		{
			tool: "semantic_search",
			args: map[string]any{"query": "where is the invoice rounded", "limit": 5},
			rpcs: []string{"Searcher/SemanticSearch"},
			req:  `{"query":"where is the invoice rounded","limit":5,"meaning":true}`,
			want: `[{"path":"billing/invoice.go","line":42,"segment":"lines","start":40,` +
				`"end":58,"excerpt":"rounds the «invoice» total",` +
				`"text":"\treturn roundInvoiceTotal(lines)"},` +
				`{"path":"geo/route.go","line":7,"segment":"lines","start":7,"end":19,` +
				`"excerpt":"shortest route","text":"func shortestRoute(a, b Place) []Place {"},` +
				`{"path":"notebooks/fit.ipynb","line":1,"segment":"cells","start":3,"end":3,` +
				`"excerpt":"fit the model"},` +
				`{"path":"docs/invoice.md","line":1,"segment":"name"}]`,
		},
		{
			tool: "syntax_query",
			args: map[string]any{"uri": srcMain, "query": "(package_clause) @pkg",
				"captures": []any{"pkg"}},
			rpcs: []string{"Syntax/Query"},
			req: `{"uri":"file:///src/main.go","query":"(package_clause) @pkg",` +
				`"captureNames":["pkg"]}`,
			want: `[{"file":"file:///src/main.go","text":"package main",` +
				`"from_x":0,"from_y":0,"to_x":12,"to_y":0,"capture_name":"pkg"}]`,
		},
		{
			tool: "syntax_query_node", args: map[string]any{"uri": srcMain,
				"node_types": "namespace|type"},
			rpcs: []string{"Syntax/QueryNode"},
			// NodeCaptureDefinitionNamespace | NodeCaptureDefinitionType.
			req: `{"uri":"file:///src/main.go","nodeTypes":66}`,
			want: `[{"file":"file:///src/main.go","text":"main",` +
				`"from_x":8,"from_y":0,"to_x":12,"to_y":0,` +
				`"capture_name":"definition.namespace"}]`,
		},
		{
			tool: "syntax_search",
			args: map[string]any{
				"query":     "(function_declaration) @fn",
				"captures":  []any{"fn"},
				"languages": []any{"go", "rust"},
			},
			rpcs: []string{"Syntax/Search"},
			req: `{"query":"(function_declaration) @fn","captureNames":["fn"],` +
				`"languages":["go","rust"]}`,
			want: `[{"file":"file:///src/main.go","text":"func main()",` +
				`"from_x":0,"from_y":5,"to_x":12,"to_y":5,"capture_name":"fn"}]`,
		},
		{
			tool: "syntax_search_node", args: map[string]any{"node_types": "func|method"},
			rpcs: []string{"Syntax/SearchNode"},
			// NodeCaptureDefinitionFunc | NodeCaptureDefinitionMethod.
			req: `{"nodeTypes":40}`,
			want: `[{"file":"file:///src/main.go","text":"MyFunc",` +
				`"from_x":5,"from_y":10,"to_x":11,"to_y":10,` +
				`"capture_name":"definition.func"}]`,
		},
	}
}

// sameJSON reports whether a and b are equal JSON documents, as require.JSONEq does, for
// use off the test goroutine.
func sameJSON(a, b string) bool {
	var av, bv any
	if json.Unmarshal([]byte(a), &av) != nil || json.Unmarshal([]byte(b), &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func TestMCPProcessCallEveryTool(t *testing.T) {
	env, c := startMCPProcess(t)
	cases := processCases()
	var names []string
	for _, tc := range cases {
		names = append(names, tc.tool)
	}
	require.Equal(t, slices.Sorted(maps.Keys(processListTools(t, c))), names,
		"a case for every listed tool, sorted")

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			env.rpcs.take()
			res, err := processCall(t, c, tc.tool, tc.args)
			require.NoError(t, err)
			got := processText(t, res)
			require.False(t, res.IsError, got)
			require.JSONEq(t, tc.want, got)
			if isBareSymbolCall(tc.args) {
				require.Contains(t, processHint(t, res), "has no qualifier")
			} else {
				require.Empty(t, processHint(t, res))
			}

			rpcs := env.rpcs.take()
			var methods []string
			for _, r := range rpcs {
				methods = append(methods, r.method)
			}
			require.Equal(t, tc.rpcs, methods)
			req, err := protojson.Marshal(rpcs[len(rpcs)-1].req)
			require.NoError(t, err)
			require.JSONEq(t, tc.req, string(req))
		})
	}
}

// The symbol tools resolve a qualified name through the workspace's symbol index.
func TestMCPProcessIndexSymbol(t *testing.T) {
	env, c := startMCPProcess(t)
	env.rpcs.take()
	res, err := processCall(t, c, "lsp_definition", map[string]any{"symbol": "pkg.Symbol"})
	require.NoError(t, err)
	require.False(t, res.IsError, processText(t, res))
	var methods []string
	rpcs := env.rpcs.take()
	for _, r := range rpcs {
		methods = append(methods, r.method)
	}
	require.Equal(t, []string{"Syntax/ResolveSymbol", "LSP/Definition"}, methods)
	req, err := protojson.Marshal(rpcs[1].req)
	require.NoError(t, err)
	require.JSONEq(t, `{"textDocument":{"uri":"file:///src/main.go"},`+
		`"position":{"line":10,"character":5}}`, string(req))
}

func TestMCPProcessErrors(t *testing.T) {
	env, c := startMCPProcess(t)

	toolError := func(t *testing.T, tool string, args map[string]any, want string) []rpc {
		t.Helper()
		env.rpcs.take()
		res, err := processCall(t, c, tool, args)
		require.NoError(t, err)
		require.True(t, res.IsError, "want a tool error")
		require.Contains(t, processText(t, res), want)
		return env.rpcs.take()
	}

	t.Run("missing argument", func(t *testing.T) {
		rpcs := toolError(t, "lsp_definition", map[string]any{}, `"symbol"`)
		require.Empty(t, rpcs, "no host RPC for an invalid call")
	})
	t.Run("invalid position", func(t *testing.T) {
		rpcs := toolError(t, "lsp_completion", atPos(srcMain, -1, 0),
			`argument "line" must be a whole number`)
		require.Empty(t, rpcs)
	})
	t.Run("invalid node type", func(t *testing.T) {
		rpcs := toolError(t, "syntax_search_node",
			map[string]any{"node_types": "func|nope"}, `"nope"`)
		require.Empty(t, rpcs)
	})
	t.Run("unknown symbol", func(t *testing.T) {
		rpcs := toolError(t, "lsp_definition", map[string]any{"symbol": "Nope"},
			`symbol "Nope" not found`)
		require.Len(t, rpcs, 2)
		require.Equal(t, "Syntax/ResolveSymbol", rpcs[0].method)
		require.Equal(t, "LSP/WorkspaceSymbol", rpcs[1].method)
	})
	t.Run("unknown tool", func(t *testing.T) {
		_, err := processCall(t, c, "no_such_tool", map[string]any{})
		require.ErrorContains(t, err, "no_such_tool")
	})
	t.Run("the process survives errors", func(t *testing.T) {
		res, err := processCall(t, c, "lsp_diagnostics", map[string]any{"uri": srcMain})
		require.NoError(t, err)
		require.False(t, res.IsError, processText(t, res))
	})
	t.Run("host gone", func(t *testing.T) {
		env.srv.Stop()
		res, err := processCall(t, c, "lsp_diagnostics", map[string]any{"uri": srcMain})
		require.NoError(t, err)
		require.True(t, res.IsError, "a host failure is a tool error")
		require.Contains(t, processText(t, res), "code = Unavailable")

		// The server still answers what needs no host.
		require.Len(t, processListTools(t, c), len(mcpTools))
	})
}

func TestMCPProcessConcurrentCalls(t *testing.T) {
	_, c := startMCPProcess(t)
	cases := processCases()
	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers*len(cases))
	for w := range workers {
		wg.Go(func() {
			for i := range cases {
				tc := cases[(i+w*7)%len(cases)]
				var req mcp.CallToolRequest
				req.Params.Name = tc.tool
				req.Params.Arguments = tc.args
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				res, err := c.CallTool(ctx, req)
				cancel()
				if err != nil {
					errs <- fmt.Errorf("%s: %w", tc.tool, err)
					continue
				}
				if len(res.Content) == 0 {
					errs <- fmt.Errorf("%s: no content", tc.tool)
					continue
				}
				text, _ := res.Content[0].(mcp.TextContent)
				if res.IsError || !sameJSON(tc.want, text.Text) {
					errs <- fmt.Errorf("%s: got %s", tc.tool, text.Text)
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
