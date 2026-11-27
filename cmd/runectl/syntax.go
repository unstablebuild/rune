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
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/cli/mcptools"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func syntaxIterToGeneric(
	sit iterator.Iterator[syntaxapi.Result],
) iterator.Iterator[mcptools.SearchResult] {
	return iterator.FromFunc(
		func(ctx context.Context) (
			mcptools.SearchResult, bool, error,
		) {
			r, ok := sit.Next(ctx)
			if !ok {
				return mcptools.SearchResult{}, false,
					sit.Err()
			}
			return mcptools.SearchResult{
				File:        fmt.Sprint(r.File),
				Text:        r.Text,
				FromX:       r.From.X,
				FromY:       r.From.Y,
				ToX:         r.To.X,
				ToY:         r.To.Y,
				CaptureName: r.CaptureName,
			}, true, nil
		},
		sit.Close,
	)
}

func printSyntaxResults(
	ctx context.Context,
	format string,
	sit iterator.Iterator[syntaxapi.Result],
) error {
	defer func() { _ = sit.Close() }()
	it := syntaxIterToGeneric(sit)
	defer func() { _ = it.Close() }()
	if format == "" {
		for {
			r, ok := it.Next(ctx)
			if !ok {
				return it.Err()
			}
			fmt.Printf(
				"%s %s %d:%d-%d:%d %s\n",
				r.File, r.Text,
				r.FromY, r.FromX,
				r.ToY, r.ToX,
				r.CaptureName,
			)
		}
	}
	return printIterator(ctx, format, it, []string{
		"File", "Text",
		"FromX", "FromY", "ToX", "ToY",
		"CaptureName",
	})
}

func newSyntaxCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "syntax",
		Short: "AST-level search commands",
	}

	cmd.AddCommand(
		newSyntaxSearchCmd(a),
		newSyntaxSearchNodeCmd(a),
		newSyntaxQueryCmd(a),
		newSyntaxQueryNodeCmd(a),
		newSyntaxResolveCmd(a),
		newSyntaxSymbolsCmd(a),
	)

	return cmd
}

func newSyntaxSearchCmd(a *app) *cobra.Command {
	var format string
	var captures []string
	var languages []string

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search workspace using a tree-sitter query",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			sit, err := w.Parser(cmd.Context()).Search(
				args[0], captures, languages...,
			)
			if err != nil {
				return err
			}
			return printSyntaxResults(cmd.Context(), format, sit)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().StringArrayVarP(
		&captures, "capture", "c", nil,
		"Capture name filter (repeatable)",
	)
	cmd.Flags().StringArrayVarP(
		&languages, "lang", "L", nil,
		"Language filter (repeatable, e.g. go, python)",
	)

	return cmd
}

func newSyntaxSearchNodeCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "searchnode <node-type>",
		Short: "Search workspace for known node types",
		Long: `Search workspace for known node types.

Node types: scope|namespace|reference|func|var|method|type`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			nodeType, err := mcptools.ParseNodeType(args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			sit, err := w.Parser(cmd.Context()).SearchNode(nodeType)
			if err != nil {
				return err
			}
			return printSyntaxResults(cmd.Context(), format, sit)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

type resolveMatch struct {
	URI        string `json:"uri"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Display    string `json:"display"`
	ImportPath string `json:"import_path"`
}

// stderrProgress reports ResolveSymbol progress to stderr so it does not
// pollute the command's stdout output.
type stderrProgress struct{}

func (stderrProgress) Report(msg string, found int, step, total int64) {
	if total > 0 {
		fmt.Fprintf(
			os.Stderr, "%s (found %d, %d/%d)\n",
			msg, found, step, total,
		)
		return
	}
	fmt.Fprintf(os.Stderr, "%s (found %d)\n", msg, found)
}

func resolveIterToGeneric(
	mit iterator.Iterator[syntaxapi.Match],
) iterator.Iterator[resolveMatch] {
	return iterator.FromFunc(
		func(ctx context.Context) (resolveMatch, bool, error) {
			m, ok := mit.Next(ctx)
			if !ok {
				return resolveMatch{}, false, mit.Err()
			}
			return resolveMatch{
				URI:        m.URI,
				X:          m.Pos.X,
				Y:          m.Pos.Y,
				Display:    m.Display,
				ImportPath: m.ImportPath,
			}, true, nil
		},
		mit.Close,
	)
}

func printResolveMatches(
	ctx context.Context,
	format string,
	mit iterator.Iterator[syntaxapi.Match],
) error {
	defer func() { _ = mit.Close() }()
	it := resolveIterToGeneric(mit)
	defer func() { _ = it.Close() }()
	if format == "" {
		for {
			m, ok := it.Next(ctx)
			if !ok {
				return it.Err()
			}
			fmt.Printf(
				"%s %d:%d %s %s\n",
				m.URI, m.Y, m.X, m.Display, m.ImportPath,
			)
		}
	}
	return printIterator(ctx, format, it, []string{
		"URI", "X", "Y", "Display", "ImportPath",
	})
}

func newSyntaxResolveCmd(a *app) *cobra.Command {
	var format string
	var quiet bool

	cmd := &cobra.Command{
		Use:   "resolve <symbol>",
		Short: "Resolve a dotted symbol name to its locations",
		Long: `Resolve a dotted symbol name (e.g. "pkg.Symbol") to the
locations that define or reference it across the workspace.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			var progress syntaxapi.Progress
			if !quiet {
				progress = stderrProgress{}
			}
			mit, err := w.Parser(cmd.Context()).ResolveSymbol(
				cmd.Context(), args[0], progress,
			)
			if err != nil {
				return err
			}
			return printResolveMatches(cmd.Context(), format, mit)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().BoolVarP(
		&quiet, "quiet", "q", false,
		"Suppress progress reporting on stderr",
	)

	return cmd
}

type referencedSymbol struct {
	Name string `json:"name"`
}

func newSyntaxSymbolsCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "symbols",
		Short: "List fully qualified referenced symbols in the workspace",
		Long: `List the fully qualified names (e.g. "pkg.Symbol") of every
package/module-qualified symbol referenced or defined across the workspace.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			sit, err := w.Parser(cmd.Context()).ListReferencedSymbols(
				cmd.Context(),
			)
			if err != nil {
				return err
			}
			if format == "" {
				return iterator.ForEach(
					cmd.Context(), sit,
					func(name string) error {
						fmt.Println(name)
						return nil
					},
				)
			}
			it := iterator.Map(sit, func(name string) referencedSymbol {
				return referencedSymbol{Name: name}
			})
			defer func() { _ = it.Close() }()
			return printIterator(
				cmd.Context(), format, it, []string{"Name"},
			)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func newSyntaxQueryCmd(a *app) *cobra.Command {
	var format string
	var captures []string

	cmd := &cobra.Command{
		Use:   "query <file> <query>",
		Short: "Query a file using a tree-sitter query",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			sit, err := w.Parser(cmd.Context()).Query(
				uri, args[1], captures,
			)
			if err != nil {
				return err
			}
			return printSyntaxResults(cmd.Context(), format, sit)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)
	cmd.Flags().StringArrayVarP(
		&captures, "capture", "c", nil,
		"Capture name filter (repeatable)",
	)

	return cmd
}

func newSyntaxQueryNodeCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "querynode <file> <node-type>",
		Short: "Query a file for known node types",
		Long: `Query a file for known node types.

Node types: scope|namespace|reference|func|var|method|type`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			uri, err := a.resolveURIArg(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			nodeType, err := mcptools.ParseNodeType(args[1])
			if err != nil {
				return err
			}
			w, err := a.getWorkspace()
			if err != nil {
				return err
			}
			sit, err := w.Parser(cmd.Context()).QueryNode(uri, nodeType)
			if err != nil {
				return err
			}
			return printSyntaxResults(cmd.Context(), format, sit)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}
