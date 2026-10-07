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
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// defaultProfileDir is where tsgo writes profiles when no directory is
// given. It is a path on the workspace host, where tsgo runs, so it
// cannot come from the extension's own environment.
const defaultProfileDir = "/tmp/rune-tsgo-profiles"

// maxServers bounds the requests that drain one answer from each
// running TypeScript server.
const maxServers = 64

var errNoFile = errors.New("no file open; open a file first")

// sourceDefinitionCmd jumps to the JavaScript implementation of the
// symbol at the cursor, where definition stops at its .d.ts.
type sourceDefinitionCmd struct {
	lsp    semanticapi.LSP
	editor textapi.Editor
	wm     browserapi.WindowManager
	opener browserapi.ResourceOpener
	notify browserapi.Notifications
}

var _ textapi.CommandHandler = (*sourceDefinitionCmd)(nil)

func (c *sourceDefinitionCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource == nil {
		return errNoFile
	}
	params := semanticapi.TextDocumentPositionParams{
		TextDocument: lspcmd.TextDocID(cmd.URI),
		Position:     lspcmd.CoordToPos(cmd.Cursor.Content),
	}
	res, err := execRequest[semanticapi.LocationResult](ctx, c.lsp,
		"custom/textDocument/sourceDefinition", params)
	if err != nil {
		return err
	}
	locs := locationEntries(res)
	if len(locs) == 0 {
		_, _ = c.notify.Notify(browserapi.LevelInfo, "No source definition for the symbol at the cursor")
		return nil
	}
	win, err := c.wm.Focus()
	if err != nil {
		return err
	}
	return openLocation(c.editor, c.wm, c.opener, win, cmd.URI, locs[0])
}

func (c *sourceDefinitionCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// projectInfoParams is the params of tsgo's custom/projectInfo request.
type projectInfoParams struct {
	TextDocument semanticapi.TextDocumentIdentifier `json:"textDocument"`
}

// projectInfo is tsgo's answer to custom/projectInfo.
type projectInfo struct {
	// ConfigFilePath is the tsconfig.json or jsconfig.json of the
	// project tsgo checks the file in, empty for an inferred project.
	ConfigFilePath string `json:"configFilePath"`
}

// projectConfigCmd opens the tsconfig.json or jsconfig.json of the
// project tsgo checks the current file in.
type projectConfigCmd struct {
	lsp    semanticapi.LSP
	editor textapi.Editor
	wm     browserapi.WindowManager
	opener browserapi.ResourceOpener
	notify browserapi.Notifications
}

var _ textapi.CommandHandler = (*projectConfigCmd)(nil)

func (c *projectConfigCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource == nil {
		return errNoFile
	}
	info, err := execRequest[projectInfo](ctx, c.lsp, "custom/projectInfo",
		projectInfoParams{TextDocument: lspcmd.TextDocID(cmd.URI)})
	if err != nil {
		return err
	}
	if info.ConfigFilePath == "" {
		_, _ = c.notify.Notify(browserapi.LevelInfo,
			"No tsconfig.json or jsconfig.json includes %s; it is checked as an inferred project",
			cmd.URI.Name())
		return nil
	}
	win, err := c.wm.Focus()
	if err != nil {
		return err
	}
	return openLocation(c.editor, c.wm, c.opener, win, cmd.URI,
		semanticapi.Location{URI: "file://" + info.ConfigFilePath})
}

func (c *projectConfigCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

type profileResult struct {
	File string `json:"file"`
}

type profileParams struct {
	Dir string `json:"dir"`
}

// profileDir resolves the optional directory argument of a profiling
// command against the workspace root.
func profileDir(wsDir string, args []string) string {
	if len(args) == 0 || args[0] == "" {
		return defaultProfileDir
	}
	if filepath.IsAbs(args[0]) {
		return args[0]
	}
	return filepath.Join(wsDir, args[0])
}

// cpuProfileCmd starts and stops CPU profiling. Without a server ID to
// target one root, start reaches every TypeScript server, so stop
// collects the profile of each.
type cpuProfileCmd struct {
	lsp    semanticapi.LSP
	notify browserapi.Notifications
	wsDir  string
}

var _ textapi.CommandHandler = (*cpuProfileCmd)(nil)

func (c *cpuProfileCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if len(cmd.Args) == 0 {
		return fmt.Errorf("usage: ts cpu-profile start [<dir>] | stop")
	}
	switch cmd.Args[0] {
	case "start":
		dir := profileDir(c.wsDir, cmd.Args[1:])
		if _, err := execRequest[json.RawMessage](ctx, c.lsp, "custom/startCPUProfile",
			profileParams{Dir: dir}); err != nil {
			return err
		}
		_, _ = c.notify.Notify(browserapi.LevelInfo, "CPU profiling started; profiles go to %s", dir)
		return nil
	case "stop":
		var files []string
		var err error
		for range maxServers {
			var res profileResult
			res, err = execRequest[profileResult](ctx, c.lsp, "custom/stopCPUProfile", nil)
			if err != nil || res.File == "" {
				break
			}
			files = append(files, res.File)
		}
		if len(files) == 0 {
			return err
		}
		_, _ = c.notify.Notify(browserapi.LevelInfo, "CPU profile saved to %s", strings.Join(files, ", "))
		return nil
	}
	return fmt.Errorf("unknown cpu-profile action %q; use start or stop", cmd.Args[0])
}

func (c *cpuProfileCmd) Complete(_ context.Context, _ string, args []string) (iterator.Iterator[string], error) {
	if len(args) <= 1 {
		prefix := ""
		if len(args) == 1 {
			prefix = args[0]
		}
		return iterator.FromSlice(filterNames([]string{"start", "stop"}, prefix)), nil
	}
	return iterator.Empty[string](), nil
}

// saveProfileCmd writes a heap or allocation profile of the first
// TypeScript server that answers.
type saveProfileCmd struct {
	lsp    semanticapi.LSP
	notify browserapi.Notifications
	wsDir  string
	method string
	what   string
}

var _ textapi.CommandHandler = (*saveProfileCmd)(nil)

func (c *saveProfileCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	res, err := execRequest[profileResult](ctx, c.lsp, c.method,
		profileParams{Dir: profileDir(c.wsDir, cmd.Args)})
	if err != nil {
		return err
	}
	_, _ = c.notify.Notify(browserapi.LevelInfo, "%s saved to %s", c.what, res.File)
	return nil
}

func (c *saveProfileCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

type gcCmd struct {
	lsp    semanticapi.LSP
	notify browserapi.Notifications
}

var _ textapi.CommandHandler = (*gcCmd)(nil)

func (c *gcCmd) HandleCommand(ctx context.Context, _ textapi.Command) error {
	if _, err := execRequest[json.RawMessage](ctx, c.lsp, "custom/runGC", nil); err != nil {
		return err
	}
	_, _ = c.notify.Notify(browserapi.LevelInfo, "Ran the garbage collector of the TypeScript language servers")
	return nil
}

func (c *gcCmd) Complete(context.Context, string, []string) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// execRequest sends a tsgo custom request to the TypeScript servers and
// decodes the first non-null answer into T, leaving T zero for null.
// Targeting them by server ID keeps the request from other languages'
// servers, which would fail it.
func execRequest[T any](ctx context.Context, lsp semanticapi.LSP, method string, params any) (T, error) {
	var zero T
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return zero, fmt.Errorf("marshal %s params: %w", method, err)
		}
		raw = data
	}
	result, err := lsp.ExecuteRequest(ctx, semanticapi.ExecuteRequestParams{
		Method:   method,
		Params:   raw,
		ServerID: tsLanguageID,
	})
	if err != nil {
		return zero, fmt.Errorf("%s: %s", method, lspErrorMessage(err))
	}
	if len(result) == 0 || string(result) == "null" {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(result, &out); err != nil {
		return zero, fmt.Errorf("decode %s result: %w", method, err)
	}
	return out, nil
}

// lspErrorMessage extracts the server-reported message, which gRPC's
// status rendering ("rpc error: code = Unknown desc = ...") buries.
func lspErrorMessage(err error) string {
	if s, ok := status.FromError(err); ok {
		return s.Message()
	}
	return err.Error()
}

// openLocation opens the file at loc in win and moves the cursor to the
// start of loc's range. win must be captured before any floating window
// opens, since a float holds the focus while it is up. base is any URI
// of the workspace, used to rebase the server's file:// location onto
// the workspace scheme.
func openLocation(
	editor textapi.Editor, wm browserapi.WindowManager,
	opener browserapi.ResourceOpener, win browserapi.Window,
	base workspaceapi.URI, loc semanticapi.Location,
) error {
	uri, err := lspcmd.LspToURI(base, loc.URI)
	if err != nil {
		return err
	}
	h, err := opener.Open(uri)
	if err != nil {
		return fmt.Errorf("open %s: %w", uri.Name(), err)
	}
	// The window content must be the opener's handler: the browser
	// recognizes only its own tokens.
	if err := wm.SetWindowContent(win, h); err != nil && !errors.Is(err, browserapi.ErrTabNotFree) {
		return err
	}
	eh, err := editor.Editor(uri)
	if err != nil {
		return err
	}
	target := lspcmd.PosToCoord(loc.Range.Start)
	if err := editor.SetCursor(eh, target); err != nil {
		if cur, curErr := editor.Cursor(eh); curErr == nil && cur == target {
			return nil
		}
		return err
	}
	return nil
}

// locationEntries flattens a LocationResult into Locations.
func locationEntries(r semanticapi.LocationResult) []semanticapi.Location {
	var out []semanticapi.Location
	if r.Location != nil {
		out = append(out, *r.Location)
	}
	out = append(out, r.Locations...)
	for _, ll := range r.LocationLinks {
		out = append(out, semanticapi.Location{URI: ll.TargetURI, Range: ll.TargetSelectionRange})
	}
	return out
}
