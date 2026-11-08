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

package extension

import (
	"context"
	"fmt"

	"unstable.build/rune/cmd/rune-agent/agentshell"
	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
)

// mcpCommands backs the agent shell's mcp commands with the handler's
// MCP servers.
type mcpCommands struct {
	*runemcp.Manager
	h *aiEditorHandler
}

var _ agentshell.MCPInfo = mcpCommands{}

func (c mcpCommands) Login(ctx context.Context, name string, out func(string)) error {
	return c.Manager.Login(ctx, name, c.h.openURL, out)
}

// Trust asks again whether to trust the server, and connects it if the
// user does.
func (c mcpCommands) Trust(ctx context.Context, name string) error {
	raw, ok := c.h.mcpServers[name]
	if !ok {
		return fmt.Errorf("no MCP server named %q", name)
	}
	cfg, err := c.h.prepareMCPServer(ctx, name, raw)
	if err != nil {
		return err
	}
	tools, err := c.ConnectServer(ctx, name, cfg)
	if err != nil {
		return err
	}
	c.h.replaceMCPTools(name, tools, nil)
	return nil
}
