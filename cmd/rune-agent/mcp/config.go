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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Transport kinds returned by ServerConfig.Transport.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
	TransportSSE   = "sse"
)

// configSource says where a server was declared, which decides whether
// connecting to it needs the user's trust.
type configSource uint8

const (
	sourceWorkspace configSource = iota
	sourceUser
)

// ServerConfig describes how to connect to a single MCP server.
type ServerConfig struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	// Headers are sent with every request to a remote server's origin.
	Headers map[string]string `json:"headers"`
	// HeadersHelper is a shell command whose stdout is a JSON object of
	// headers, merged over Headers before each connect.
	HeadersHelper string `json:"headersHelper"`
	// Timeout bounds a single tool call, in milliseconds. Values under
	// 1000 are ignored.
	Timeout int          `json:"timeout"`
	OAuth   *OAuthConfig `json:"oauth"`

	source configSource
}

// OAuthConfig overrides how Rune signs in to a remote server.
type OAuthConfig struct {
	// ClientID is a client pre-registered with the authorization
	// server. It takes precedence over automatic registration.
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	// CallbackPort pins the loopback redirect port, for clients
	// registered with an exact redirect URI. Zero picks a free port.
	CallbackPort int `json:"callbackPort"`
	// Scopes, space separated, replaces the scopes Rune would discover.
	Scopes string `json:"scopes"`
	// AuthServerMetadataURL replaces authorization server metadata
	// discovery.
	AuthServerMetadataURL string `json:"authServerMetadataUrl"`
}

// FromWorkspace reports whether the server was declared by the
// workspace rather than by the user's own configuration.
func (c ServerConfig) FromWorkspace() bool { return c.source == sourceWorkspace }

// Transport returns the transport kind, one of TransportStdio,
// TransportHTTP or TransportSSE. Type may be omitted when Command or
// URL alone decides it.
func (c ServerConfig) Transport() (string, error) {
	if c.Command != "" && c.URL != "" {
		return "", errors.New("server config sets both command and url")
	}
	switch c.Type {
	case "", TransportStdio:
		if c.Command != "" {
			return TransportStdio, nil
		}
		if c.Type == "" && c.URL != "" {
			return TransportHTTP, nil
		}
		return "", errors.New("server config has no command")
	case TransportHTTP, "streamable-http", "streamableHttp":
		if c.URL == "" {
			return "", fmt.Errorf("%s server config has no url", c.Type)
		}
		return TransportHTTP, nil
	case TransportSSE:
		if c.URL == "" {
			return "", fmt.Errorf("%s server config has no url", c.Type)
		}
		return TransportSSE, nil
	default:
		return "", fmt.Errorf("unsupported server type %q", c.Type)
	}
}

// ParseServers decodes the user configuration's mcpServers map, which
// uses the same schema as .mcp.json.
func ParseServers(raw map[string]any) (map[string]ServerConfig, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var servers map[string]ServerConfig
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, err
	}
	return servers, nil
}

// Merge combines the servers from the user configuration with the ones
// the workspace declares. A workspace server replaces a user server of
// the same name.
func Merge(user, workspace map[string]ServerConfig) map[string]ServerConfig {
	merged := make(map[string]ServerConfig, len(user)+len(workspace))
	for name, cfg := range user {
		cfg.source = sourceUser
		merged[name] = cfg
	}
	for name, cfg := range workspace {
		cfg.source = sourceWorkspace
		merged[name] = cfg
	}
	return merged
}

// hasHeader reports whether headers sets name, ignoring case.
func hasHeader(headers map[string]string, name string) bool {
	for k := range headers {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// Config holds the contents of a .mcp.json file.
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// LoadConfig parses a .mcp.json file from raw bytes.
func LoadConfig(data []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
