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

package texttest

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"

	"unstable.build/rune/internal/ide/idecmd"
	"unstable.build/rune/internal/text"
)

// dispatchWithAliases is a texttest-local helper that mirrors what
// ide/ex does in production: it wires a text.Component to an
// idecmd.Expander built from the alias map installed on the
// Component's config, then iterates the expanded command stream and
// dispatches each yielded command.
//
// Tests use it in place of comp.DispatchCommand so they continue to
// exercise alias resolution after the loop moved out of text/.
func dispatchWithAliases(
	ctx context.Context, c *text.Component, cmd textapi.Command,
) (bool, error) {
	exp := idecmd.NewExpander(c.CommandAliases(), c.DispatchEnv(), nil)
	ctx = idecmd.WithChain(ctx, cmd.Name, idecmd.NewChain())
	it, err := exp.Expand(ctx, cmd)
	if err != nil {
		return false, err
	}
	defer func() { _ = it.Close() }()
	var handled bool
	for {
		next, ok := it.Next(ctx)
		if !ok {
			break
		}
		h, derr := c.DispatchCommand(ctx, next)
		if derr != nil {
			return h, derr
		}
		handled = handled || h
	}
	return handled, it.Err()
}
