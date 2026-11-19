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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestSwitchSourceHeader(t *testing.T) {
	t.Run("opens the counterpart in the focused window", func(t *testing.T) {
		f := newActionFixture(t, cppActionCmdName)
		f.lsp.requests["textDocument/switchSourceHeader"] = json.RawMessage(`"file:///ws/src/main.hpp"`)
		require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("switch", 0, 0)))
		assert.Equal(t, []string{"/ws/src/main.hpp"}, f.opener.openedPaths())
		shown, ok := f.wm.shown().(*stubResource)
		require.True(t, ok, "the opener's handler must become the window content")
		assert.Equal(t, "/ws/src/main.hpp", shown.uri.Path())
		assert.Empty(t, f.notify.notifMessages())
	})
	t.Run("no counterpart hints", func(t *testing.T) {
		f := newActionFixture(t, cActionCmdName)
		require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("switch", 0, 0)))
		assert.Empty(t, f.opener.openedPaths())
		assert.True(t, f.notify.hasMessage("No source or header pairs with main.cpp"))
	})
}

func TestRenderAST(t *testing.T) {
	rng := func(sl, sc, el, ec uint32) *semanticapi.Range {
		return &semanticapi.Range{
			Start: semanticapi.Position{Line: sl, Character: sc},
			End:   semanticapi.Position{Line: el, Character: ec},
		}
	}
	node := astNode{Role: "statement", Kind: "Return", Range: rng(2, 2, 2, 11), Children: []astNode{
		{Role: "expression", Kind: "BinaryOperator", Detail: "+", Range: rng(2, 9, 2, 14), Children: []astNode{
			{Role: "expression", Kind: "DeclRef", Detail: "a", Range: rng(2, 9, 2, 10)},
			{Role: "expression", Kind: "IntegerLiteral", Detail: "1", Range: rng(2, 13, 2, 14)},
		}},
	}}
	var b strings.Builder
	renderAST(&b, node, 0)
	assert.Equal(t, strings.Join([]string{
		"Return [3:3-3:12]",
		"  BinaryOperator + [3:10-3:15]",
		"    DeclRef a [3:10-3:11]",
		"    IntegerLiteral 1 [3:14-3:15]",
		"",
	}, "\n"), b.String())
}

func TestASTCommand(t *testing.T) {
	t.Run("floats the tree of the node at the cursor", func(t *testing.T) {
		f := newActionFixture(t, cActionCmdName)
		f.lsp.requests["textDocument/ast"] = json.RawMessage(
			`{"role":"declaration","kind":"Function","detail":"main","range":{"start":{"line":0,"character":0},"end":{"line":3,"character":1}}}`)
		require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("ast", 1, 2)))
		frame := renderFloating(t, f.wm.floated(), 60, 10)
		assert.Contains(t, frame, "Function main [1:1-4:2]")

		f.lsp.mu.Lock()
		defer f.lsp.mu.Unlock()
		var params astParams
		require.NoError(t, json.Unmarshal(f.lsp.requested[0].Params, &params))
		require.NotNil(t, params.Range, "without a selection the cursor is sent as a collapsed range")
		assert.Equal(t, semanticapi.Position{Line: 1, Character: 2}, params.Range.Start)
		assert.Equal(t, params.Range.Start, params.Range.End)
	})
	t.Run("null answer hints", func(t *testing.T) {
		f := newActionFixture(t, cActionCmdName)
		require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("ast", 1, 2)))
		assert.Nil(t, f.wm.floated())
		assert.True(t, f.notify.hasMessage("No AST node covers the selection"))
	})
}

func TestSymbolInfoCommand(t *testing.T) {
	f := newActionFixture(t, cppActionCmdName)
	f.lsp.requests["textDocument/symbolInfo"] = json.RawMessage(
		`[{"name":"area","containerName":"geo::Shape::","usr":"c:@N@geo@S@Shape@F@area#1","id":"ABCDEF0123456789"}]`)
	require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("symbol-info", 4, 8)))
	frame := renderFloating(t, f.wm.floated(), 80, 10)
	assert.Contains(t, frame, "Name: area")
	assert.Contains(t, frame, "Container: geo::Shape::")
	assert.Contains(t, frame, "USR: c:@N@geo@S@Shape@F@area#1")
	assert.Contains(t, frame, "ID: ABCDEF0123456789")

	f = newActionFixture(t, cppActionCmdName)
	f.lsp.requests["textDocument/symbolInfo"] = json.RawMessage(`[]`)
	require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("symbol-info", 4, 8)))
	assert.Nil(t, f.wm.floated())
	assert.True(t, f.notify.hasMessage("No symbol at the cursor"))
}

func TestMemoryTreeUnmarshalAndRender(t *testing.T) {
	var tree memoryTree
	require.NoError(t, json.Unmarshal([]byte(`{
		"_self": 100, "_total": 5000,
		"background_index": {"_self": 0, "_total": 3000, "slabs": {"_self": 3000, "_total": 3000}},
		"tuscheduler": {"_self": 0, "_total": 1900,
			"/ws/a.cpp": {"_self": 1500, "_total": 1500},
			"/ws/b.cpp": {"_self": 400, "_total": 400}}
	}`), &tree))
	assert.Equal(t, int64(100), tree.Self)
	assert.Equal(t, int64(5000), tree.Total)
	require.Len(t, tree.Children, 2)

	var b strings.Builder
	renderMemory(&b, "clangd", tree, 0)
	assert.Equal(t, strings.Join([]string{
		"clangd 4.9 KiB (self 100 B)",
		"  background_index 2.9 KiB",
		"    slabs 2.9 KiB",
		"  tuscheduler 1.9 KiB",
		"    /ws/a.cpp 1.5 KiB",
		"    /ws/b.cpp 400 B",
		"",
	}, "\n"), b.String())
}

func TestMemoryUsageCommand(t *testing.T) {
	f := newActionFixture(t, cActionCmdName)
	f.lsp.requests["$/memoryUsage"] = json.RawMessage(`{"_self":0,"_total":2048,"preamble":{"_self":2048,"_total":2048}}`)
	require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("memory-usage", 0, 0)))
	frame := renderFloating(t, f.wm.floated(), 60, 10)
	assert.Contains(t, frame, "clangd 2.0 KiB")
	assert.Contains(t, frame, "preamble 2.0 KiB")
	assert.Equal(t, []string{"$/memoryUsage"}, f.lsp.requestedMethods())

	f = newActionFixture(t, cActionCmdName)
	require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("memory-usage", 0, 0)))
	assert.True(t, f.notify.hasMessage("clangd is not running"))
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "0 B", humanBytes(0))
	assert.Equal(t, "1023 B", humanBytes(1023))
	assert.Equal(t, "1.0 KiB", humanBytes(1024))
	assert.Equal(t, "1.5 MiB", humanBytes(1536*1024))
	assert.Equal(t, "2.0 GiB", humanBytes(2<<30))
}

func TestFencedCode(t *testing.T) {
	assert.Equal(t, "```\nint x;\n```", fencedCode("int x;\n", ""))
	assert.Equal(t, "````c\na ``` b\n````", fencedCode("a ``` b", "c"))
}

// renderFloating resizes the floating handler to w x h, draws it and
// returns the frame as text, one line per row.
func renderFloating(t *testing.T, f browserapi.Floating, w, h int) string {
	t.Helper()
	require.NotNil(t, f, "no floating window was shown")
	f.Resize(w, h)
	return handlertest.DrawHandler(f, w, h)
}
