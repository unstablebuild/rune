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
	"fmt"

	"github.com/spf13/cobra"
)

func newResolveCmd(a *app) *cobra.Command {
	var (
		format string
		dir    string
	)

	cmd := &cobra.Command{
		Use:   "resolve [flags] <command>",
		Short: "Print the file the workspace executor runs for a command",
		Long: `Print the file the workspace executor runs for a command.

The command is resolved against the workspace executor's PATH, the way
exec would run it.

Examples:
  runectl resolve go
  runectl resolve --dir /tmp ./script.sh
  runectl resolve -F json gopls`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()

			w, err := a.getWorkspace()
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			executor := w.Executor(ctx)
			defer func() { _ = executor.Close() }()

			path, err := executor.LookPath(ctx, args[0], dir)
			if err != nil {
				return fmt.Errorf("resolve: %w", err)
			}
			return printString(format, path, []string{"Path"})
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().StringVarP(
		&dir, "dir", "d", "",
		"Working directory to resolve a relative command against",
	)
	return cmd
}
