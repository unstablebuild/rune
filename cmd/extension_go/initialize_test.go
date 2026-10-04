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
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

func TestGoplsCommand(t *testing.T) {
	cases := []struct {
		name string
		dbg  goplsDebugOptions
		bin  string
		want string
	}{
		{"default", goplsDebugOptions{}, "", "gopls serve"},
		{"debug logging", goplsDebugOptions{LogLevel: "debug"}, "",
			"gopls -v serve"},
		{"trace logging", goplsDebugOptions{LogLevel: "trace"}, "",
			"gopls -vv serve"},
		{"rpc trace only", goplsDebugOptions{RPCTrace: true}, "", "gopls -rpc.trace serve"},
		{"logfile only", goplsDebugOptions{LogFile: "/tmp/gopls.log"}, "",
			"gopls -logfile=/tmp/gopls.log serve"},
		{"debug addr only", goplsDebugOptions{DebugAddr: "localhost:6060"}, "",
			"gopls -debug=localhost:6060 serve"},
		{"all flags", goplsDebugOptions{
			RPCTrace:  true,
			LogFile:   "auto",
			DebugAddr: "localhost:6060",
		}, "", "gopls -rpc.trace -logfile=auto -debug=localhost:6060 serve"},
		{"custom bin path", goplsDebugOptions{}, "/opt/gopls", "/opt/gopls serve"},
		{"custom bin path with flags",
			goplsDebugOptions{RPCTrace: true, LogFile: "/tmp/g.log"},
			"/opt/gopls",
			"/opt/gopls -rpc.trace -logfile=/tmp/g.log serve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, goplsCommand(tc.dbg, tc.bin))
		})
	}
}

func TestGoplsInitializeParamsCommandAndTrace(t *testing.T) {
	params, err := goplsInitializeParams("file:///tmp/repo", goplsDebugOptions{
		RPCTrace:  true,
		LogFile:   "/tmp/gopls.log",
		DebugAddr: "localhost:6060",
		Trace:     semanticapi.TraceValueVerbose,
	}, "")
	require.NoError(t, err)
	assert.Equal(t, semanticapi.TraceValueVerbose, params.Trace)

	var initOpts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &initOpts))
	assert.Equal(t,
		"gopls -rpc.trace -logfile=/tmp/gopls.log -debug=localhost:6060 serve",
		initOpts["command"])
}

func TestReadGoplsDebugOptions(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		assert.Equal(t, goplsDebugOptions{}, readGoplsDebugOptions(nil))
	})

	t.Run("missing debug key", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"other": "value"})
		assert.Equal(t, goplsDebugOptions{}, readGoplsDebugOptions(cfg))
	})

	t.Run("all fields populated", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{
			"debug": map[string]any{
				"rpc_trace": true,
				"logfile":   "/tmp/gopls.log",
				"addr":      "localhost:6060",
				"trace":     "verbose",
				"log_level": "debug",
			},
		})
		assert.Equal(t, goplsDebugOptions{
			RPCTrace:  true,
			LogFile:   "/tmp/gopls.log",
			DebugAddr: "localhost:6060",
			Trace:     semanticapi.TraceValueVerbose,
			LogLevel:  "debug",
		}, readGoplsDebugOptions(cfg))
	})

	t.Run("invalid trace falls back to off", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{
			"debug": map[string]any{"trace": "bogus"},
		})
		assert.Equal(t, goplsDebugOptions{}, readGoplsDebugOptions(cfg))
	})
}

func TestResolveLogFile(t *testing.T) {
	t.Run("empty passthrough", func(t *testing.T) {
		got, err := resolveLogFile("")
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("auto passthrough", func(t *testing.T) {
		got, err := resolveLogFile("auto")
		require.NoError(t, err)
		assert.Equal(t, "auto", got)
	})

	t.Run("expands tilde and creates parent dir", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		if runtime.GOOS == "windows" {
			t.Setenv("USERPROFILE", tmp)
		}

		got, err := resolveLogFile("~/sub/nested/gopls.log")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(tmp, "sub", "nested", "gopls.log"), got)
		st, err := os.Stat(filepath.Join(tmp, "sub", "nested"))
		require.NoError(t, err)
		assert.True(t, st.IsDir())
	})

	t.Run("absolute path creates parent dir", func(t *testing.T) {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "a", "b", "gopls.log")
		got, err := resolveLogFile(path)
		require.NoError(t, err)
		assert.Equal(t, path, got)
		st, err := os.Stat(filepath.Join(tmp, "a", "b"))
		require.NoError(t, err)
		assert.True(t, st.IsDir())
	})
}

func TestReadGoplsDebugOptionsLogFileMissingParent(t *testing.T) {
	tmp := t.TempDir()
	// Make tmp read-only so MkdirAll fails for any subdirectory.
	require.NoError(t, os.Chmod(tmp, 0o500))
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })

	cfg := config.JSONFromMap(map[string]any{
		"debug": map[string]any{
			"logfile": filepath.Join(tmp, "nope", "gopls.log"),
		},
	})
	got := readGoplsDebugOptions(cfg)
	assert.Empty(t, got.LogFile,
		"unusable logfile path must be cleared rather than forwarded to gopls")
}

func TestReadGoplsDebugOptionsLogFileTildeExpansion(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}

	cfg := config.JSONFromMap(map[string]any{
		"debug": map[string]any{
			"logfile": "~/.rune/logs/lsp/gopls.log",
		},
	})
	got := readGoplsDebugOptions(cfg)
	assert.Equal(t,
		filepath.Join(tmp, ".rune", "logs", "lsp", "gopls.log"),
		got.LogFile)
}
