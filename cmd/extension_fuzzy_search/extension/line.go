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
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	tconfig "unstable.build/rune/internal/config"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/workspace/walkdir"
)

const lineHistoryDocumentID = "extension-fuzzy-line-history"

var (
	lineDefaultHistoryKey = term.KeyComb{Ch: '\\', Mod: term.ModCtrl}
	cmdSearchText         = textapi.CommandManual{
		Name: "searchtext",
		Summary: "Opens a new window to perform a fuzzy search for file contents in the workspace. " +
			"Results are sorted by match score in descending order. " +
			"Arrow keys and <ctrl-k>/<ctrl-j> scroll up and down and <enter> opens up the selected file in a new tab. " +
			"By default, a built-in implementation is used to scan for files in the workspace, but " +
			"for very large workspaces, a program like ripgrep or the silver searcher " +
			"can be used by adding the corresponding 'command' key in the extension's " +
			"configuration or by passing an argument (i.e. searchText rg --color never -n --no-heading --max-columns 500 \"\"" +
			"). To scroll back to previous searches, " +
			`<ctrl-\> can be used by default or a 'history_key' can be set in the extension's ` +
			"configuration. Ctrl-c can be used to cancel a scan in progress.",
		Synopsis: "[<command>]",
	}
)

func readfiles(cwd workspaceapi.FileSystem, ctx context.Context) (
	iterator.Iterator[string], error,
) {
	it, err := walkdir.ListFiles(ctx, cwd, ".")
	if err != nil {
		return nil, err
	}
	return walkdir.ReadLines(ctx, cwd, it)
}

func lineResource(workspace workspaceapi.FileSystem, data string) (
	workspaceapi.URI, term.Coordinates, bool,
) {
	chunks := strings.Split(data, ":")
	if len(chunks) < 2 {
		return workspaceapi.URI{}, term.Coordinates{}, false
	}
	y, _ := strconv.Atoi(chunks[1])
	name := chunks[0]
	uri, err := workspace.URI(name)
	return uri, term.Coordinates{Y: y - 1}, err == nil
}

func newLineHandler(
	ctx context.Context, cmd textapi.Command,
	clients finder.Clients, invokeWindow browserapi.Window, c config.Config,
) (finder.RedispatchHandler, error) {
	cmdStr, err := c.GetString("command")
	if err != nil {
		if err != config.ErrNotFound {
			log.Printf("failed to load 'command' config: %v", err)
		}
	}
	historyKey, err := tconfig.GetKey(c, "history_key")
	if err != nil {
		if err != config.ErrNotFound {
			log.Printf("failed to load 'history_key' config: %v", err)
		}
		historyKey = lineDefaultHistoryKey
	}
	if len(cmd.Args) != 0 {
		cmdStr = strings.Join(cmd.Args, " ")
	}
	return finder.New(ctx, clients, invokeWindow,
		c, historyKey, lineHistoryDocumentID, cmdStr, readfiles, lineResource)
}
