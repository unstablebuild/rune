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
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func newStorageCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Document storage commands",
	}

	cmd.AddCommand(
		newStorageCreateCmd(a),
		newStorageSetCmd(a),
		newStorageGetCmd(a),
		newStorageUpdateCmd(a),
		newStorageDeleteCmd(a),
		newStorageListCmd(a),
	)

	return cmd
}

func newStorageCreateCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "create <id> <json-doc>",
		Short: "Create a document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			var doc map[string]any
			if err := json.Unmarshal(
				[]byte(args[1]), &doc,
			); err != nil {
				return fmt.Errorf("invalid JSON: %w", err)
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Storage(cmd.Context()).Create(cmd.Context(), args[0], doc)
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

func newStorageSetCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "set <id> <json-doc>",
		Short: "Create or update a document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			var doc map[string]any
			if err := json.Unmarshal(
				[]byte(args[1]), &doc,
			); err != nil {
				return fmt.Errorf("invalid JSON: %w", err)
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Storage(cmd.Context()).Set(cmd.Context(), args[0], doc)
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

func newStorageGetCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			if format == "table" {
				return fmt.Errorf(
					"table format not supported" +
						" for storage get",
				)
			}
			var doc map[string]any
			err = w.Storage(cmd.Context()).Get(cmd.Context(), args[0], &doc)
			if err != nil {
				return err
			}
			f := format
			if f == "" {
				f = "json"
			}
			it := iterator.FromSlice([]map[string]any{doc})
			return printIterator(cmd.Context(), f, it, nil)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newStorageUpdateCmd(a *app) *cobra.Command {
	var format string
	var precond string

	cmd := &cobra.Command{
		Use:   "update <id> <field.path> <value> [<field.path> <value> ...]",
		Short: "Update document fields",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			if len(args) < 3 || (len(args)-1)%2 != 0 {
				return fmt.Errorf(
					"usage: storage update [options] <id>" +
						" <field.path> <value>" +
						" [<field.path> <value> ...]",
				)
			}
			id := args[0]
			updates, err := parseUpdateArgs(args[1:])
			if err != nil {
				return err
			}
			var preconds []storageapi.Precondition
			if precond != "" {
				preconds, err = parsePreconditions(precond)
				if err != nil {
					return err
				}
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Storage(cmd.Context()).Update(
				cmd.Context(), id, updates, preconds...,
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
	cmd.Flags().StringVarP(
		&precond, "preconditions", "p", "",
		"JSON preconditions",
	)

	return cmd
}

func newStorageDeleteCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			err = w.Storage(cmd.Context()).Delete(cmd.Context(), args[0])
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

func newStorageListCmd(a *app) *cobra.Command {
	var format string
	var filters []string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List documents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			if format == "table" {
				return fmt.Errorf(
					"table format not supported" +
						" for storage list",
				)
			}
			parsedFilters, err := parseFilters(filters)
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			sit, err := w.Storage(cmd.Context()).List(cmd.Context(), parsedFilters)
			if err != nil {
				return err
			}
			defer func() { _ = sit.Close() }()

			it := storageIterToGeneric(cmd.Context(), sit)
			defer func() { _ = it.Close() }()

			f := format
			if f == "" {
				f = "json"
			}
			return printIterator(cmd.Context(), f, it, nil)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().StringArrayVarP(
		&filters, "filter", "f", nil,
		"JSON filter (repeatable)",
	)

	return cmd
}

func parseUpdateArgs(
	args []string,
) ([]storageapi.Update, error) {
	if len(args)%2 != 0 {
		return nil, fmt.Errorf(
			"update requires pairs of" +
				" <field.path> <value>",
		)
	}
	updates := make(
		[]storageapi.Update, 0, len(args)/2,
	)
	for i := 0; i < len(args); i += 2 {
		path := strings.Split(args[i], ".")
		if slices.Contains(path, "") {
			return nil, fmt.Errorf(
				"empty segment in field path %q",
				args[i],
			)
		}
		var value any
		if err := json.Unmarshal(
			[]byte(args[i+1]), &value,
		); err != nil {
			value = args[i+1]
		}
		updates = append(updates, storageapi.Update{
			FieldPath: path, Value: value,
		})
	}
	return updates, nil
}

type jsonPrecondition struct {
	Path  []string `json:"path"`
	Value any      `json:"value"`
}

func parsePreconditions(
	s string,
) ([]storageapi.Precondition, error) {
	var raw []jsonPrecondition
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf(
			"invalid preconditions JSON: %w", err,
		)
	}
	ret := make([]storageapi.Precondition, len(raw))
	for i, p := range raw {
		ret[i] = storageapi.Precondition{
			FieldPath: p.Path, Value: p.Value,
		}
	}
	return ret, nil
}

type jsonFilter struct {
	Path  []string `json:"path"`
	Op    string   `json:"op"`
	Value any      `json:"value"`
}

func parseFilters(
	raw []string,
) ([]storageapi.Filter, error) {
	var ret []storageapi.Filter
	for _, s := range raw {
		var f jsonFilter
		if err := json.Unmarshal(
			[]byte(s), &f,
		); err != nil {
			return nil, fmt.Errorf(
				"invalid filter JSON %q: %w", s, err,
			)
		}
		ret = append(ret, storageapi.Filter{
			Field: storageapi.Field{
				FieldPath: f.Path,
				Value:     f.Value,
			},
			Op: storageapi.Op(f.Op),
		})
	}
	return ret, nil
}

func storageIterToGeneric(
	_ context.Context,
	sit storageapi.Iterator,
) iterator.Iterator[map[string]any] {
	return iterator.FromFunc(
		func(ctx context.Context) (
			map[string]any, bool, error,
		) {
			if !sit.HasNext() {
				return nil, false, nil
			}
			var doc map[string]any
			if err := sit.NextTo(&doc); err != nil {
				return nil, false, err
			}
			return doc, true, nil
		},
		sit.Close,
	)
}
