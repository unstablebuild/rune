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
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/oauth2"
)

var errNotInRune = errors.New( //nolint:staticcheck
	"This CLI must be invoked from one of" +
		" Rune's plugin commands (! or !!)",
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(exit.code)
		}
		fmt.Fprintf(os.Stderr, "runectl: %s\n", err)
		os.Exit(1)
	}
}

type app struct {
	workspace *extensionapi.Workspace
}

func (a *app) getWorkspace() (
	*extensionapi.Workspace, error,
) {
	if a.workspace != nil {
		return a.workspace, nil
	}
	socket := os.Getenv("RUNE_SOCKET")
	if socket == "" {
		return nil, errNotInRune
	}
	datadir := os.Getenv("RUNE_DATADIR")
	if datadir == "" {
		return nil, errNotInRune
	}
	cfg := extensionapi.Config{
		Socket:  socket,
		DataDir: datadir,
	}
	if tok := os.Getenv("RUNE_TOKEN"); tok != "" {
		cfg.Token = &oauth2.Token{AccessToken: tok}
	}
	if cert := os.Getenv("RUNE_CERT"); cert != "" {
		data, err := base64.StdEncoding.DecodeString(cert)
		if err != nil {
			return nil, fmt.Errorf("decode cert: %w", err)
		}
		cfg.Certificate = data
	}
	meta := extensionapi.Metadata{
		ExtensionID: "runectl",
	}
	w, err := extensionapi.NewWorkspace(cfg, meta)
	if err != nil {
		return nil, err
	}
	a.workspace = w
	return w, nil
}

func looksLikeURI(s string) bool {
	return strings.Contains(s, "://")
}

func (a *app) resolveURIArg(
	ctx context.Context, arg string,
) (workspaceapi.URI, error) {
	if looksLikeURI(arg) {
		return workspaceapi.ParseURI(arg)
	}
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return workspaceapi.URI{}, fmt.Errorf(
			"resolve path: %w", err,
		)
	}
	w, err := a.getWorkspace()
	if err != nil {
		return workspaceapi.URI{}, err
	}
	return w.FileSystem(ctx).URI(absPath)
}

func newRootCmd() *cobra.Command {
	a := &app{}
	var verbose bool

	cmd := &cobra.Command{
		Use:   "runectl",
		Short: "CLI for the Rune IDE",
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: false,
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(
			cmd *cobra.Command, args []string,
		) {
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(
				slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
					Level: level,
				}),
			))
		},
	}

	cmd.PersistentFlags().BoolVarP(
		&verbose, "verbose", "v", false,
		"Enable debug logging",
	)

	cmd.AddCommand(
		newDatadirCmd(a),
		newURICmd(a),
		newOpenCmd(a),
		newNotifyCmd(a),
		newWMCmd(a),
		newStorageCmd(a),
		newEditorCmd(a),
		newSyntaxCmd(a),
		newExecCmd(a),
		newResolveCmd(a),
		newSignalCmd(a),
		newLSPCmd(a),
		newLLMCmd(a),
		newSearchCmd(a),
		newMCPCmd(a),
		newStatusCmd(),
		newScreenshotCmd(),
	)

	return cmd
}
