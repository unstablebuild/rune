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
	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
)

func newOpenCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "open <uri>",
		Short: "Open a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			parsed, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			h, err := w.ResourceOpener(cmd.Context()).Open(parsed)
			if err != nil {
				return err
			}
			uri := args[0]
			if tok, ok := h.(browserrpc.Token); ok {
				uri = tok.URI
			}
			return printString(
				format, uri, []string{"Resource"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}
