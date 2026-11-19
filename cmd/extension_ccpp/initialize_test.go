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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClangdCommand(t *testing.T) {
	cases := []struct {
		name string
		opts clangdOptions
		want string
	}{
		{
			name: "defaults",
			opts: clangdOptions{bin: "/data/bin/clangd", logLevel: "error"},
			want: "/data/bin/clangd --background-index --clang-tidy --header-insertion=iwyu --log=error",
		},
		{
			name: "user args come last so they override the defaults",
			opts: clangdOptions{bin: "clangd", logLevel: "verbose",
				args: []string{"--query-driver=/usr/bin/arm-none-eabi-g++", "--clang-tidy=false"}},
			want: "clangd --background-index --clang-tidy --header-insertion=iwyu --log=verbose " +
				"--query-driver=/usr/bin/arm-none-eabi-g++ --clang-tidy=false",
		},
		{
			name: "the compilation database travels in the options, not the command",
			opts: clangdOptions{bin: "clangd", logLevel: "error", compileCommandsDir: "/ws/build dir"},
			want: "clangd --background-index --clang-tidy --header-insertion=iwyu --log=error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, clangdCommand(tc.opts))
		})
	}
}

func TestClangdInitializeParams(t *testing.T) {
	params, err := clangdInitializeParams("file:///ws", "ws", clangdOptions{
		bin:                "/data/bin/clangd",
		compileCommandsDir: "/ws/build",
		fallbackFlags:      []string{"-std=c++20", "-Iinclude"},
		logLevel:           "info",
	})
	require.NoError(t, err)
	assert.Equal(t, "file:///ws", params.RootURI)
	require.Len(t, params.WorkspaceFolders, 1)
	assert.Equal(t, "file:///ws", params.WorkspaceFolders[0].URI)
	assert.Equal(t, "ws", params.WorkspaceFolders[0].Name)

	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t, "ccpp", opts["langID"], "one server id serves c and cpp")
	assert.Equal(t,
		"/data/bin/clangd --background-index --clang-tidy --header-insertion=iwyu --log=info",
		opts["command"])
	assert.Equal(t, "/ws/build", opts["compilationDatabasePath"])
	assert.Equal(t, []any{"-std=c++20", "-Iinclude"}, opts["fallbackFlags"])
	assert.Equal(t, false, opts["clangdFileStatus"])

	// Without a database or flags the keys stay absent so clangd keeps
	// its per-file lookup; an empty log level takes the default.
	defaults, err := clangdInitializeParams("file:///ws", "ws", clangdOptions{bin: "clangd"})
	require.NoError(t, err)
	var opts2 map[string]any
	require.NoError(t, json.Unmarshal(defaults.InitializeOptions, &opts2))
	_, hasDB := opts2["compilationDatabasePath"]
	assert.False(t, hasDB)
	_, hasFlags := opts2["fallbackFlags"]
	assert.False(t, hasFlags)
	assert.Equal(t, "clangd --background-index --clang-tidy --header-insertion=iwyu --log=error",
		opts2["command"])
}

func TestClangdInitializeCapabilities(t *testing.T) {
	params, err := clangdInitializeParams("file:///ws", "ws", clangdOptions{bin: "clangd"})
	require.NoError(t, err)
	var caps map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &caps))

	textDocument, _ := caps["textDocument"].(map[string]any)
	require.NotNil(t, textDocument)

	completion, _ := textDocument["completion"].(map[string]any)
	require.NotNil(t, completion)
	_, hasItem := completion["completionItem"]
	assert.False(t, hasItem, "no snippet support may be advertised: the editor inserts completions verbatim")

	codeAction, _ := textDocument["codeAction"].(map[string]any)
	require.NotNil(t, codeAction)
	_, literal := codeAction["codeActionLiteralSupport"]
	assert.True(t, literal, "clangd answers code actions as bare commands without literal support")

	for _, wanted := range []string{
		"hover", "definition", "declaration", "typeDefinition", "implementation",
		"references", "documentSymbol", "signatureHelp", "formatting", "rangeFormatting",
		"rename", "documentHighlight", "inlayHint", "publishDiagnostics", "foldingRange",
		"selectionRange", "semanticTokens", "callHierarchy", "typeHierarchy",
	} {
		_, has := textDocument[wanted]
		assert.True(t, has, "%s is implemented by clangd and must be advertised", wanted)
	}
	hover, _ := textDocument["hover"].(map[string]any)
	assert.Contains(t, hover["contentFormat"], "markdown")
}
