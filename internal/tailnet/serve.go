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

package tailnet

import (
	"context"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"unstable.build/rune/internal/runenet"
)

// Serve serves scheme to the machines of the tailnet c reaches that may
// open workspaces on this one, see [Authorizer], whenever the machine's
// Tailscale client is connected; see [Client.Listen]. Closing the
// returned server stops serving.
func Serve(
	c *Client, scheme schemeapi.Scheme, dataDir string, opts ...runenet.ServeOption,
) *runenet.WorkspaceServer {
	return runenet.Serve(c.Listen(), NewAuthorizer(c.lc), scheme, dataDir, opts...)
}

// Join brings node, which joins the user's tailnet as a machine of its
// own, onto the tailnet and serves scheme there to the machines that
// may open workspaces on it; see [Authorizer]. It blocks until node is
// signed in, and returns a nil server and no error when ctx ends first.
func Join(
	ctx context.Context, node *runenet.Node, scheme schemeapi.Scheme,
	dataDir string, opts ...runenet.ServeOption,
) (*runenet.WorkspaceServer, error) {
	if err := node.Start(ctx); err != nil {
		return nil, fmt.Errorf("could not join the tailnet: %w", err)
	}
	lis, err := node.Listen()
	if err != nil {
		return nil, fmt.Errorf("listen on the tailnet: %w", err)
	}
	lc, err := node.LocalClient()
	if err != nil {
		_ = lis.Close()
		return nil, err
	}
	server := runenet.Serve(lis, NewAuthorizer(lc), scheme, dataDir, opts...)
	if err := node.Up(ctx); err != nil {
		_ = server.Close()
		if ctx.Err() != nil {
			return nil, nil
		}
		return nil, fmt.Errorf("could not join the tailnet: %w", err)
	}
	return server, nil
}
