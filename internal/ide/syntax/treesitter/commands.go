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

package treesitter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/syntax"
)

// Commands returns the file-level commands the tree provides for the tab
// edited by handler.
func (t *Tree) Commands(handler syntax.Handler) ([]textapi.CommandManual, syntax.CommandHandler) {
	return commands, commandHandler{handler: handler, t: t}
}

const (
	cmdJumpToSyntax = "jumptoast"
)

var commands = []textapi.CommandManual{
	{
		Name: cmdJumpToSyntax,
		Summary: "Run the given query against the file currently in focus and jump to a " +
			"matching AST node. The first argument is the name or path of the query file to run. " +
			"The second argument is the match capture name(s), and the last argument " +
			"is the name of the node in the file (i.e. function name, variable name, etc.). " +
			"The second argument can be ORed by adding a `|` character between match names." +
			"For example to match against functions and methods you can pass: " +
			"locals.scm local.definition.method|local.definition.function MyFuncName. " +
			"The query file should be a relative or absolute path and if not found, " +
			"it will be searched in the file's language package installation " +
			"folder.",
		Synopsis: "<query> <capture>... <name>",
	},
}

type commandHandler struct {
	handler syntax.Handler
	t       *Tree
}

// HandleCommand satisfies syntax.CommandHandler.
func (c commandHandler) HandleCommand(ctx context.Context, cmd textapi.Command) (err error) {
	switch cmd.Name {
	case cmdJumpToSyntax:
		return c.handleJumpToSyntax(ctx, cmd)
	default:
		return errors.New("unknown command")
	}
}

// Complete satisfies syntax.CommandHandler.
func (c commandHandler) Complete(ctx context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	switch cmd.Name {
	case cmdJumpToSyntax:
		return c.completeJumpToSyntax(ctx, cmd)
	default:
		return nil, "", errors.New("unkown command")
	}
}

func (c commandHandler) completeJumpToSyntax(ctx context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	// complete with pre-loaded files
	if len(cmd.Args) <= 1 {
		return iterator.FromSlice([]string{"locals.scm"}), "", nil
	}

	// complete query capture names
	set := make(map[string]struct{})
	if len(cmd.Args) == 2 {
		it, err := c.t.Query(cmd.Args[0])
		if err != nil {
			return nil, "", err
		}
		matches, err := iterator.ToSlice(ctx, it)
		if err != nil {
			return nil, "", err
		}
		for _, match := range matches {
			set[match.CaptureName] = struct{}{}
		}
		sliceSet := make([]string, 0, len(set))
		for captureName := range set {
			sliceSet = append(sliceSet, captureName)
		}
		return iterator.FromSlice(sliceSet), "", nil
	}

	if len(cmd.Args) == 3 {
		queryFile, captureNameString := cmd.Args[0], cmd.Args[1]
		captureNames := strings.Split(captureNameString, "|")

		matches, err := c.t.Query(queryFile, captureNames...)
		if err != nil {
			return nil, "", err
		}
		seen := make(map[string]struct{})
		lines := iterator.Map(matches, func(s Match) string {
			// quote so that the prompt's tokenizer hands the line back
			// verbatim as a single argument: source lines routinely
			// contain quotes, backslashes and runs of whitespace.
			return command.ShellQuote(jumpToSyntaxLineString(s.LineString))
		})
		return iterator.Filter(lines, func(line string) bool {
			if _, ok := seen[line]; ok {
				return false
			}
			seen[line] = struct{}{}
			return true
		}), "", nil
	}
	return iterator.Empty[string](), "", nil
}

func jumpToSyntaxLineString(line string) string {
	return strings.TrimLeft(line, " \t")
}

func (c commandHandler) handleJumpToSyntax(
	ctx context.Context, cmd textapi.Command,
) (err error) {
	if len(cmd.Args) < 3 {
		return errors.New("expected at least three arguments")
	}
	queryFile, captureNameString, lineStringChunks := cmd.Args[0], cmd.Args[1], cmd.Args[2:]
	captureNames := strings.Split(captureNameString, "|")
	lineString := strings.Join(lineStringChunks, " ")

	matches, err := c.t.Query(queryFile, captureNames...)
	if err != nil {
		return err
	}
	defer matches.Close()
	for {
		match, ok := matches.Next(ctx)
		if !ok {
			err = fmt.Errorf("could not find matching node")
			break
		}
		if jumpToSyntaxLineString(match.LineString) == jumpToSyntaxLineString(lineString) {
			c.handler.SetCursorAtScroll(term.Coordinates{Y: match.Line})
			break
		}
	}
	if merr := matches.Err(); merr != nil {
		err = multierror.Append(err, fmt.Errorf("symbols iterator: %w", merr))
	}
	return err
}
