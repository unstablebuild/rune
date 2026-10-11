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

func TestTSInitializeParams(t *testing.T) {
	params, err := tsInitializeParams("file:///ws", "/data/typescript/bin/tsgo")
	require.NoError(t, err)
	assert.Equal(t, "file:///ws", params.RootURI)

	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t, "typescript", opts["langID"])
	assert.Equal(t, "/data/typescript/bin/tsgo --lsp --stdio", opts["command"])
	prefs, ok := opts["userPreferences"].(map[string]any)
	require.True(t, ok, "tsgo reads its preferences from userPreferences")
	assert.Equal(t, "all", prefs["includeInlayParameterNameHints"])

	var caps struct {
		TextDocument map[string]map[string]any `json:"textDocument"`
		Workspace    map[string]any            `json:"workspace"`
	}
	require.NoError(t, json.Unmarshal(params.Capabilities, &caps))
	assert.Contains(t, caps.TextDocument, "diagnostic",
		"tsgo answers per-file diagnostics only to pulls")
	assert.NotContains(t, caps.TextDocument, "declaration", "tsgo has no declarationProvider")
	assert.NotContains(t, caps.TextDocument["completion"], "completionItem",
		"snippet completions must stay off: Rune inserts completion text verbatim")
	assert.NotContains(t, caps.Workspace, "configuration",
		"advertising configuration makes tsgo ignore userPreferences")
	kinds := caps.TextDocument["codeAction"]["codeActionLiteralSupport"].(map[string]any)["codeActionKind"].(map[string]any)["valueSet"]
	assert.ElementsMatch(t, []any{
		"quickfix", "source", "source.organizeImports",
		"source.removeUnusedImports", "source.sortImports", "source.fixAll",
	}, kinds)
}
