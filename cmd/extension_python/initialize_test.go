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
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPyCommand(t *testing.T) {
	cases := []struct {
		name     string
		bin      string
		fallback string
		subcmd   string
		want     string
	}{
		{"unresolved ty", "", "ty", "server", "ty server"},
		{"resolved ty", "/opt/ty", "ty", "server", "/opt/ty server"},
		{"unresolved ruff", "", "ruff", "server", "ruff server"},
		{"resolved ruff", "/usr/local/bin/ruff", "ruff", "server", "/usr/local/bin/ruff server"},
		{"no subcmd", "/opt/uv", "uv", "", "/opt/uv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pyCommand(tc.bin, tc.fallback, tc.subcmd))
		})
	}
}

func TestPyRuffCommand(t *testing.T) {
	tests := []struct {
		name     string
		logLevel string
		want     string
	}{
		{name: "default", want: "ruff server"},
		{name: "info", logLevel: "info", want: "ruff server"},
		{name: "debug", logLevel: "debug", want: "ruff server -v"},
		{name: "trace", logLevel: "trace", want: "ruff server -v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pyRuffCommand("", tt.logLevel))
		})
	}
}

func TestPyInitializeParams(t *testing.T) {
	t.Run("advertises supported response shapes", func(t *testing.T) {
		params, err := pyInitializeParams("file:///tmp/repo", "ty server", nil, "", "")
		require.NoError(t, err)

		var capabilities map[string]any
		require.NoError(t, json.Unmarshal(params.Capabilities, &capabilities))
		textDocument := capabilities["textDocument"].(map[string]any)

		assert.Equal(t, map[string]any{
			"contentFormat": []any{"markdown", "plaintext"},
		}, textDocument["hover"])
		assert.Equal(t, map[string]any{
			"linkSupport": true,
		}, textDocument["declaration"])
		assert.Equal(t, map[string]any{
			"linkSupport": true,
		}, textDocument["definition"])
		assert.Equal(t, map[string]any{
			"linkSupport": true,
		}, textDocument["typeDefinition"])
		assert.Equal(t, map[string]any{
			"prepareSupport": true,
		}, textDocument["rename"])
		assert.Equal(t, map[string]any{
			"completionItem": map[string]any{
				"documentationFormat": []any{"markdown", "plaintext"},
			},
		}, textDocument["completion"])
		assert.Equal(t, map[string]any{
			"signatureInformation": map[string]any{
				"activeParameterSupport": true,
				"parameterInformation": map[string]any{
					"labelOffsetSupport": true,
				},
			},
		}, textDocument["signatureHelp"])
		assert.Equal(t, map[string]any{
			"relatedInformation": true,
		}, textDocument["publishDiagnostics"])
	})

	t.Run("with alternates", func(t *testing.T) {
		params, err := pyInitializeParams("file:///tmp/repo", "ty server", map[string]string{
			"textDocument/formatting":      "ruff server",
			"textDocument/rangeFormatting": "ruff server",
		}, "", "")
		require.NoError(t, err)
		assert.Equal(t, "file:///tmp/repo", params.RootURI)

		var initOpts map[string]any
		require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
		assert.Equal(t, "python", initOpts["langID"])
		assert.Equal(t, "ty server", initOpts["command"])
		assert.Equal(t, map[string]any{
			"textDocument/formatting":      "ruff server",
			"textDocument/rangeFormatting": "ruff server",
		}, initOpts["alternate_commands"])
	})

	// ty and ruff both size their rayon pool from the core count. Left
	// uncapped they saturate every core, and the editor's render loop
	// is then stuck waiting for a thread to run on.
	t.Run("caps the server worker pool at half the cores", func(t *testing.T) {
		params, err := pyInitializeParams("file:///tmp/repo", "ty server", map[string]string{
			"textDocument/formatting": "ruff server",
		}, "", "")
		require.NoError(t, err)

		var initOpts map[string]any
		require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))

		want := strconv.Itoa(max(1, runtime.NumCPU()/2))
		assert.Equal(t, map[string]any{"RAYON_NUM_THREADS": want}, initOpts["env"],
			"env reaches ruff too: alternate-command children inherit langConfig.env")
		assert.NotEqual(t, "0", want, "a zero cap would restore the default pool size")
	})

	t.Run("single server omits alternate_commands", func(t *testing.T) {
		params, err := pyInitializeParams(
			"file:///tmp/repo", "pyright-langserver --stdio", nil, "", "")
		require.NoError(t, err)

		var initOpts map[string]any
		require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
		assert.Equal(t, "pyright-langserver --stdio", initOpts["command"])
		_, ok := initOpts["alternate_commands"]
		assert.False(t, ok, "alternate_commands must be omitted in single-server mode")
	})

	t.Run("diagnostic mode injected when set", func(t *testing.T) {
		params, err := pyInitializeParams(
			"file:///tmp/repo", "ty server", nil, "workspace", "")
		require.NoError(t, err)

		var initOpts map[string]any
		require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
		assert.Equal(t, "workspace", initOpts["diagnosticMode"])
	})

	t.Run("diagnostic mode omitted when empty", func(t *testing.T) {
		params, err := pyInitializeParams("file:///tmp/repo", "ty server", nil, "", "")
		require.NoError(t, err)

		var initOpts map[string]any
		require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
		_, ok := initOpts["diagnosticMode"]
		assert.False(t, ok, "diagnosticMode must be omitted when unset")
	})

	t.Run("log level injected when set", func(t *testing.T) {
		params, err := pyInitializeParams(
			"file:///tmp/repo", "ty server", nil, "", "debug")
		require.NoError(t, err)

		var initOpts map[string]any
		require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
		assert.Equal(t, "debug", initOpts["logLevel"])
	})
}
