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
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
)

func newNotifyCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "notify <level> <message>",
		Short: "Send a notification (level: error|warn|info|success)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			level, err := parseNotificationLevel(args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			_, err = w.Notifications(cmd.Context()).Notify(level, args[1])
			if err != nil {
				return err
			}
			return printOK(format)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func parseNotificationLevel(
	s string,
) (browserapi.NotificationLevel, error) {
	switch s {
	case "error":
		return browserapi.LevelError, nil
	case "warn":
		return browserapi.LevelWarn, nil
	case "info":
		return browserapi.LevelInfo, nil
	case "success":
		return browserapi.LevelSuccess, nil
	default:
		return 0, fmt.Errorf(
			"invalid level %q: use error|warn|info|success",
			s,
		)
	}
}
