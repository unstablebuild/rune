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
	"errors"
	"fmt"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/tailnet"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerune"
)

// tailscaleNet opens tailscale:// workspaces on the machines of the
// user's own tailnet, and serves this machine's workspaces to them,
// through the Tailscale client the machine already runs. It needs no
// Rune account and no configuration: a machine connected to a tailnet
// serves, and one that is not has nothing to open.
type tailscaleNet struct {
	client     *tailnet.Client
	dataDir    string
	shellRCDir string
	packages   *servedPackages

	mu     sync.Mutex
	server *runenet.WorkspaceServer
	closed bool
}

func newTailscaleNet(
	client *tailnet.Client, dataDir, shellRCDir string, packages *servedPackages,
) *tailscaleNet {
	return &tailscaleNet{
		client:     client,
		dataDir:    dataDir,
		shellRCDir: shellRCDir,
		packages:   packages,
	}
}

func (t *tailscaleNet) serve() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.server != nil {
		return nil
	}
	scheme, dataDir, err := rootWorkspaceScheme(t.dataDir, t.shellRCDir)
	if err != nil {
		return err
	}
	t.server = tailnet.Serve(t.client, scheme, dataDir,
		runenet.WithServices(func(r grpc.ServiceRegistrar) {
			pkgrpc.NewServer(t.packages).Register(r)
		}))
	return nil
}

func (t *tailscaleNet) completer() command.Completer {
	return workspacerune.Completer(workspace.TailscaleScheme, t.client)
}

func (t *tailscaleNet) completerOption() ide.Option {
	return ide.WithWorkspaceOpenCompleter(workspace.TailscaleScheme, t.completer())
}

func (t *tailscaleNet) schemeFunc() schemeapi.SchemeFunc {
	return workspacerune.New(workspace.TailscaleScheme, t.client)
}

func (t *tailscaleNet) register(i *ide.IDE) error {
	if err := i.RegisterScheme(workspace.TailscaleScheme, t.schemeFunc()); err != nil {
		return fmt.Errorf("register %s scheme: %w", workspace.TailscaleScheme, err)
	}
	return nil
}

// Close stops serving workspaces to the tailnet and following it.
func (t *tailscaleNet) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	var err error
	if t.server != nil {
		err = t.server.Close()
		t.server = nil
	}
	return errors.Join(err, t.client.Close())
}
