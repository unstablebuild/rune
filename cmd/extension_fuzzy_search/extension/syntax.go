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
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/finder"
)

var cmdSearchSyntax = textapi.CommandManual{
	Name: "searchast",
	Summary: "Fuzzy search AST nodes by running the given query against all the files in " +
		"the workspace. The first argument is the name or path of the query file to run. " +
		"The second argument is the match capture name(s), and the last argument " +
		"is the name of the node in the file (i.e. function name, variable name, etc.). " +
		"The second argument can be ORed by adding a `|` character between match names." +
		"For example to match against functions and methods you can pass: " +
		"locals.scm local.definition.method|local.definition.function. " +
		"The query file should be a relative or absolute path and if not found, " +
		"it will be searched in the file's language package installation " +
		"folder.",
	Synopsis: "<query> <capture>...",
}

func readSymbolsFunction(
	pkgs pkgapi.Manager, parser syntaxapi.Parser, queryFile string, captureNames []string,
) func(workspaceapi.FileSystem, context.Context) (iterator.Iterator[string], error) {
	return func(fs workspaceapi.FileSystem, ctx context.Context) (
		iterator.Iterator[string], error,
	) {
		var query string
		switch queryFile {
		case "folds.scm", "indents.scm",
			"highlights.scm", "locals.scm":
			// each language package ships its own copy
		default:
			data, err := os.ReadFile(queryFile)
			if err != nil {
				return nil, fmt.Errorf("read query file: %w", err)
			}
			query, queryFile = string(data), ""
		}
		return readSymbols(ctx, fs, pkgs, parser, queryFile, query, captureNames)
	}
}

func syntaxResource(exec workspaceapi.FileSystem, data string) (
	uri workspaceapi.URI, coords term.Coordinates, ok bool,
) {
	chunks := strings.Split(data, ":")
	if len(chunks) < 2 {
		return workspaceapi.URI{}, term.Coordinates{}, false
	}
	y, _ := strconv.Atoi(chunks[1])
	name := chunks[0]
	uri, err := exec.URI(name)
	return uri, term.Coordinates{Y: y - 1}, err == nil
}

func newSyntaxHandler(
	ctx context.Context, cmd textapi.Command,
	clients finder.Clients, invokeWindow browserapi.Window, c config.Config,
	pkgs pkgapi.Manager, parser syntaxapi.Parser,
) (finder.RedispatchHandler, error) {
	if len(cmd.Args) < 2 {
		return nil, errors.New("expected at least three arguments")
	}
	queryFile, captureNameString := cmd.Args[0], cmd.Args[1]
	captureNames := strings.Split(captureNameString, "|")
	noHistoryKey := term.KeyComb{}

	return finder.New(ctx, clients, invokeWindow,
		c, noHistoryKey, "unused", "",
		readSymbolsFunction(pkgs, parser, queryFile, captureNames), syntaxResource)
}
