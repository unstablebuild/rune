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

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/cli/mcptools"
)

func newMCPCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start MCP server over stdio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			s := server.NewMCPServer("runectl", "0.1.0", server.WithRecovery())
			registerMCPTools(cmd.Context(), s, w)
			return server.ServeStdio(s)
		},
	}
	return cmd
}

// registerMCPTools registers every syntax, LSP and search tool.
func registerMCPTools(
	ctx context.Context,
	s *server.MCPServer,
	w *extensionapi.Workspace,
) {
	parser := w.Parser(ctx)
	mcptools.RegisterSyntaxTools(ctx, s, parser, mcptools.WithAllThemes())
	mcptools.RegisterLSPTools(ctx, s, w.LSP(ctx), parser, mcptools.WithAllThemes())
	mcptools.RegisterSearchTools(ctx, s, w.Search(ctx), mcptools.WithAllThemes())
}
