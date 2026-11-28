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
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func getEditorHandler(
	ctx context.Context, a *app, uriOrPath string,
) (textapi.Handler, error) {
	parsed, err := a.resolveURIArg(ctx, uriOrPath)
	if err != nil {
		return nil, err
	}
	w, err := a.getWorkspace()
	if err != nil {
		return nil, err
	}
	return w.Editor(ctx).Editor(parsed)
}

func parseLocationPriority(
	s string,
) (textapi.LocationPriority, error) {
	switch s {
	case "info":
		return textapi.LocationPriorityInfo, nil
	case "warning":
		return textapi.LocationPriorityWarning, nil
	case "error":
		return textapi.LocationPriorityError, nil
	case "critical":
		return textapi.LocationPriorityCritical, nil
	default:
		return 0, fmt.Errorf(
			"invalid priority %q:"+
				" use info|warning|error|critical", s,
		)
	}
}

func newEditorCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "editor",
		Short: "Editor commands",
	}

	cmd.AddCommand(
		newEditorPrintCmd(a),
		newEditorColorCmd(a),
		newEditorEditCmd(a),
		newEditorLocationsCmd(a),
		newEditorCursorCmd(a),
	)

	return cmd
}

func newEditorPrintCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "print <uri>",
		Short: "Print editor text content",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			cells, err := w.Editor(cmd.Context()).CellView(h).RawCells()
			if err != nil {
				return err
			}
			text := term.CellsToString(cells)
			return printString(
				format, text, []string{"Content"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newEditorColorCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "color <uri> <bg> [<fg>]",
		Short: "Set editor default colors",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			var attr term.Attributes
			attr.Bg = term.GetColor(args[1])
			if len(args) > 2 {
				attr.Fg = term.GetColor(args[2])
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Editor(cmd.Context()).SetDefaultAttributes(h, attr)
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

type editResult struct {
	FromX int    `json:"from_x"`
	FromY int    `json:"from_y"`
	ToX   int    `json:"to_x"`
	ToY   int    `json:"to_y"`
	Old   string `json:"old"`
}

func newEditorEditCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "edit <uri> <start-x> <start-y> <end-x> <end-y> <text>",
		Short: "Edit text in a buffer",
		Args:  cobra.ExactArgs(6),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			startX, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid start-x: %w", err)
			}
			startY, err := strconv.Atoi(args[2])
			if err != nil {
				return fmt.Errorf("invalid start-y: %w", err)
			}
			endX, err := strconv.Atoi(args[3])
			if err != nil {
				return fmt.Errorf("invalid end-x: %w", err)
			}
			endY, err := strconv.Atoi(args[4])
			if err != nil {
				return fmt.Errorf("invalid end-y: %w", err)
			}
			text := args[5]
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			start := term.Coordinates{X: startX, Y: startY}
			end := term.Coordinates{X: endX, Y: endY}
			from, to, old, err := w.Editor(cmd.Context()).CellEditor(
				h,
			).Edit(cmd.Context(), start, end, text)
			if err != nil {
				return err
			}
			res := editResult{
				FromX: from.X, FromY: from.Y,
				ToX: to.X, ToY: to.Y,
				Old: old,
			}
			return printResult(
				cmd.Context(), format, res,
				func(v editResult) {
					fmt.Printf(
						"%d %d %d %d %s\n",
						v.FromX, v.FromY,
						v.ToX, v.ToY, v.Old,
					)
				},
				[]string{
					"FromX", "FromY", "ToX", "ToY", "Old",
				},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newEditorLocationsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "locations",
		Short: "Location list commands",
	}

	cmd.AddCommand(
		newLocationsSetCmd(a),
		newLocationsNextCmd(a),
		newLocationsPrevCmd(a),
	)

	return cmd
}

type jsonLocation struct {
	From    jsonCoord `json:"from"`
	To      jsonCoord `json:"to"`
	Message string    `json:"message"`
}

type jsonCoord struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func newLocationsSetCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "set <uri> <priority> <id> <json-locations>",
		Short: "Set a location list",
		Long: `Set a location list.

Priority: info|warning|error|critical
JSON locations format: [{"from":{"x":N,"y":N},"to":{"x":N,"y":N},"message":"..."},...]`,
		Args: cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			priority, err := parseLocationPriority(args[1])
			if err != nil {
				return err
			}
			id := args[2]
			var locs []jsonLocation
			if err := json.Unmarshal(
				[]byte(args[3]), &locs,
			); err != nil {
				return fmt.Errorf(
					"invalid locations JSON: %w", err,
				)
			}
			locations := make([]textapi.Location, len(locs))
			for i, l := range locs {
				locations[i] = textapi.Location{
					From: term.Coordinates{
						X: l.From.X, Y: l.From.Y,
					},
					To: term.Coordinates{
						X: l.To.X, Y: l.To.Y,
					},
					Message: l.Message,
				}
			}
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Editor(cmd.Context()).SetLocationList(
				h, priority, id,
				textapi.LocationSlice(locations),
			)
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

func newLocationsNextCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "next <uri> <id>",
		Short: "Move to next location",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Editor(cmd.Context()).MoveToNextLocation(h, args[1])
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

func newLocationsPrevCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "prev <uri> <id>",
		Short: "Move to previous location",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Editor(cmd.Context()).MoveToPrevLocation(h, args[1])
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

func newEditorCursorCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cursor",
		Short: "Cursor commands",
	}

	cmd.AddCommand(
		newCursorGetCmd(a),
		newCursorSetCmd(a),
	)

	return cmd
}

func newCursorGetCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "get <uri>",
		Short: "Get cursor position",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			coords, err := w.Editor(cmd.Context()).Cursor(h)
			if err != nil {
				return err
			}
			return printResult(
				cmd.Context(), format, coords,
				func(v term.Coordinates) {
					fmt.Printf("%d %d\n", v.X, v.Y)
				},
				[]string{"X", "Y"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newCursorSetCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "set <uri> <x> <y>",
		Short: "Set cursor position",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			x, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid x: %w", err)
			}
			y, err := strconv.Atoi(args[2])
			if err != nil {
				return fmt.Errorf("invalid y: %w", err)
			}
			h, err := getEditorHandler(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Editor(cmd.Context()).SetCursor(
				h, term.Coordinates{X: x, Y: y},
			)
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
