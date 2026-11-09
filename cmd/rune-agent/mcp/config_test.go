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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    Config
		wantErr bool
	}{
		{
			name: "valid stdio server",
			data: `{
				"mcpServers": {
					"rune": {
						"type": "stdio",
						"command": "runectl",
						"args": ["mcp"],
						"env": {"FOO": "bar"}
					}
				}
			}`,
			want: Config{
				MCPServers: map[string]ServerConfig{
					"rune": {
						Type:    "stdio",
						Command: "runectl",
						Args:    []string{"mcp"},
						Env:     map[string]string{"FOO": "bar"},
					},
				},
			},
		},
		{
			name: "multiple servers",
			data: `{
				"mcpServers": {
					"a": {"type": "stdio", "command": "a-cmd"},
					"b": {"type": "stdio", "command": "b-cmd", "args": ["--flag"]}
				}
			}`,
			want: Config{
				MCPServers: map[string]ServerConfig{
					"a": {Type: "stdio", Command: "a-cmd"},
					"b": {Type: "stdio", Command: "b-cmd", Args: []string{"--flag"}},
				},
			},
		},
		{
			name: "empty servers",
			data: `{"mcpServers": {}}`,
			want: Config{MCPServers: map[string]ServerConfig{}},
		},
		{
			name: "empty object",
			data: `{}`,
			want: Config{},
		},
		{
			name:    "malformed JSON",
			data:    `{invalid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadConfig([]byte(tt.data))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestServerConfigTransport(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ServerConfig
		want    string
		wantErr string
	}{
		{name: "stdio", cfg: ServerConfig{Type: "stdio", Command: "x"}, want: TransportStdio},
		{name: "stdio inferred", cfg: ServerConfig{Command: "x"}, want: TransportStdio},
		{name: "http", cfg: ServerConfig{Type: "http", URL: "https://x"}, want: TransportHTTP},
		{name: "streamable-http", cfg: ServerConfig{Type: "streamable-http", URL: "https://x"}, want: TransportHTTP},
		{name: "streamableHttp", cfg: ServerConfig{Type: "streamableHttp", URL: "https://x"}, want: TransportHTTP},
		{name: "http inferred", cfg: ServerConfig{URL: "https://x"}, want: TransportHTTP},
		{name: "sse", cfg: ServerConfig{Type: "sse", URL: "https://x"}, want: TransportSSE},
		{name: "websocket", cfg: ServerConfig{Type: "ws", URL: "wss://x"}, wantErr: `unsupported server type "ws"`},
		{name: "both", cfg: ServerConfig{Command: "x", URL: "https://x"}, wantErr: "both command and url"},
		{name: "stdio without command", cfg: ServerConfig{Type: "stdio"}, wantErr: "no command"},
		{name: "stdio with url only", cfg: ServerConfig{Type: "stdio", URL: "https://x"}, wantErr: "no command"},
		{name: "http without url", cfg: ServerConfig{Type: "http"}, wantErr: "no url"},
		{name: "empty", cfg: ServerConfig{}, wantErr: "no command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cfg.Transport()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseServers(t *testing.T) {
	got, err := ParseServers(map[string]any{
		"linear": map[string]any{
			"type":    "http",
			"url":     "https://mcp.linear.app/mcp",
			"headers": map[string]any{"X-Team": "core"},
			"timeout": 30000,
			"oauth": map[string]any{
				"clientId":              "abc",
				"clientSecret":          "${SECRET}",
				"callbackPort":          8765,
				"scopes":                "read write",
				"authServerMetadataUrl": "https://auth.example.com/meta",
			},
		},
		"helper": map[string]any{
			"url":           "https://example.com/mcp",
			"headersHelper": "./get-token.sh",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]ServerConfig{
		"linear": {
			Type:    "http",
			URL:     "https://mcp.linear.app/mcp",
			Headers: map[string]string{"X-Team": "core"},
			Timeout: 30000,
			OAuth: &OAuthConfig{
				ClientID:              "abc",
				ClientSecret:          "${SECRET}",
				CallbackPort:          8765,
				Scopes:                "read write",
				AuthServerMetadataURL: "https://auth.example.com/meta",
			},
		},
		"helper": {URL: "https://example.com/mcp", HeadersHelper: "./get-token.sh"},
	}, got)

	_, err = ParseServers(map[string]any{"bad": map[string]any{"args": "not-a-list"}})
	require.Error(t, err)
}

func TestMerge(t *testing.T) {
	user := map[string]ServerConfig{
		"shared": {URL: "https://user.example.com"},
		"mine":   {URL: "https://mine.example.com"},
	}
	workspace := map[string]ServerConfig{
		"shared": {URL: "https://workspace.example.com"},
		"repo":   {Command: "repo-mcp"},
	}

	got := Merge(user, workspace)
	require.Len(t, got, 3)
	assert.Equal(t, "https://workspace.example.com", got["shared"].URL, "the workspace entry wins")
	assert.True(t, got["shared"].FromWorkspace())
	assert.True(t, got["repo"].FromWorkspace())
	assert.False(t, got["mine"].FromWorkspace())

	cfg, err := LoadConfig([]byte(`{"mcpServers":{"x":{"url":"https://x"}}}`))
	require.NoError(t, err)
	assert.True(t, cfg.MCPServers["x"].FromWorkspace(), "a server not known to be the user's needs trust")
}
