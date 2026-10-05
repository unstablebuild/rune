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

package idelsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func TestParseAlternateCommands(t *testing.T) {
	t.Parallel()

	t.Run("absent yields nil", func(t *testing.T) {
		t.Parallel()
		got, err := parseAlternateCommands(map[string]any{
			"langID":  "python",
			"command": "ty server",
		})
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("well-formed map parses", func(t *testing.T) {
		t.Parallel()
		got, err := parseAlternateCommands(map[string]any{
			"alternate_commands": map[string]any{
				"textDocument/formatting":      "ruff server",
				"textDocument/rangeFormatting": "ruff server",
			},
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"textDocument/formatting":      "ruff server",
			"textDocument/rangeFormatting": "ruff server",
		}, got)
	})

	t.Run("wrong container type errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseAlternateCommands(map[string]any{
			"alternate_commands": "ruff server",
		})
		require.EqualError(t, err,
			"'alternate_commands' should be a map of LSP method to command")
	})

	t.Run("non-string value errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseAlternateCommands(map[string]any{
			"alternate_commands": map[string]any{
				"textDocument/formatting": 42,
			},
		})
		require.EqualError(t, err,
			"'alternate_commands.textDocument/formatting' should be a command string")
	})
}

func TestParseLanguageEnv(t *testing.T) {
	t.Parallel()

	t.Run("absent yields nil", func(t *testing.T) {
		t.Parallel()
		got, err := parseLanguageEnv(map[string]any{})
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("well-formed map parses", func(t *testing.T) {
		t.Parallel()
		got, err := parseLanguageEnv(map[string]any{
			"env": map[string]any{
				"Z_VAR":  "last",
				"RA_LOG": "info",
			},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"RA_LOG=info", "Z_VAR=last"}, got)
	})

	t.Run("wrong container type errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseLanguageEnv(map[string]any{"env": "RA_LOG=info"})
		require.EqualError(t, err,
			"'env' should be a map of environment variable to value")
	})

	t.Run("non-string value errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseLanguageEnv(map[string]any{
			"env": map[string]any{"RA_LOG": true},
		})
		require.EqualError(t, err,
			"'env.RA_LOG' should be a string")
	})

	t.Run("invalid name errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseLanguageEnv(map[string]any{
			"env": map[string]any{"BAD=NAME": "value"},
		})
		require.EqualError(t, err,
			"'env.BAD=NAME' is not a valid environment variable name")
	})

	t.Run("null byte in value errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseLanguageEnv(map[string]any{
			"env": map[string]any{"RA_LOG": "info\x00debug"},
		})
		require.EqualError(t, err,
			"'env.RA_LOG' contains a null byte")
	})
}

func TestChildName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		want    string
	}{
		{"ty server", "ty"},
		{"ruff server", "ruff"},
		{"/usr/local/bin/ruff", "ruff"},
		{"pyright-langserver --stdio", "pyright-langserver"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, childName(tt.command))
		})
	}
}

// multiBinPkgManager returns a fixed set of binary paths regardless
// of the requested package id, mirroring how a single Python package
// ships several executables (ty, ruff) in one lib dir.
type multiBinPkgManager struct {
	paths []string
}

func (p *multiBinPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(p.paths), nil
}

func TestFindBinaryDisambiguatesByCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tyPath := filepath.Join(dir, "ty")
	ruffPath := filepath.Join(dir, "ruff")
	require.NoError(t, os.WriteFile(tyPath, []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(ruffPath, []byte("#!/bin/sh\n"), 0o755))

	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil,
		&multiBinPkgManager{paths: []string{tyPath, ruffPath}},
		nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	tyBin, err := m.findBinary(t.Context(), &langConfig{id: "python", command: "ty"})
	require.NoError(t, err)
	assert.Equal(t, tyPath, tyBin)

	ruffBin, err := m.findBinary(t.Context(), &langConfig{id: "python", command: "ruff"})
	require.NoError(t, err)
	assert.Equal(t, ruffPath, ruffBin)
}

func TestBuildChildRunsTheHostBinary(t *testing.T) {
	t.Parallel()
	const hostPath = "/home/studio/.rune/pkg/go/1.24.0/bin/gopls"
	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil,
		&multiBinPkgManager{paths: []string{"/home/studio/.rune/pkg/go/1.24.0/bin/go", hostPath}},
		nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	srv := m.buildChild(t.Context(), langConfig{id: "go", command: "gopls"},
		"gopls", uri.String(), semanticapi.InitializeParams{})
	assert.Equal(t, hostPath, srv.binPath)
}
