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

// Package headless runs rune-agent non-interactively against a live
// Rune host.
//
// "Headless" means "not running as an extension"; it does not mean "not
// running as a plugin". Rune plugin commands (! and !!) export
// RUNE_SOCKET, RUNE_DATADIR, RUNE_INSTALLDIR, RUNE_CERT and RUNE_TOKEN,
// so a headless process can build the whole dependency graph from
// extensionapi.NewWorkspace and reuse the host's provider auth, LSP and
// syntax parser.
package headless

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"golang.org/x/oauth2"
)

// ExtensionID identifies headless runs to the host. It is deliberately
// distinct from the served extension's ID so that the client-side
// storage partition is isolated and headless dialogues never surface in
// the user's chat list.
const ExtensionID = "rune-agent-headless"

// ErrNotInRune reports that the process was not started from a Rune
// plugin command and so has no host to connect back to.
var ErrNotInRune = errors.New(
	"must be invoked from a Rune plugin command (! or !!)")

// Connect builds a Workspace from the plugin environment exported by the
// Rune host. extensionID partitions the Workspace's storage. The returned
// Workspace is lazy: no RPC happens until a capability is used.
func Connect(extensionID string) (*extensionapi.Workspace, error) {
	socket := os.Getenv("RUNE_SOCKET")
	if socket == "" {
		return nil, ErrNotInRune
	}
	datadir := os.Getenv("RUNE_DATADIR")
	if datadir == "" {
		return nil, ErrNotInRune
	}
	cfg := extensionapi.Config{
		Socket:     socket,
		DataDir:    datadir,
		InstallDir: os.Getenv("RUNE_INSTALLDIR"),
	}
	if tok := os.Getenv("RUNE_TOKEN"); tok != "" {
		cfg.Token = &oauth2.Token{AccessToken: tok}
	}
	if cert := os.Getenv("RUNE_CERT"); cert != "" {
		data, err := base64.StdEncoding.DecodeString(cert)
		if err != nil {
			return nil, fmt.Errorf("decode RUNE_CERT: %w", err)
		}
		cfg.Certificate = data
	}
	return extensionapi.NewWorkspace(cfg, extensionapi.Metadata{
		ExtensionID: extensionID,
	})
}
