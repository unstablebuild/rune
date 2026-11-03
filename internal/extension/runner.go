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
	"io"

	"github.com/unstablebuild/rune-go-sdk/api/config"
)

// Runner abstracts the ability to run and stop extensions.
type Runner interface {
	io.Closer
	Run(id, cmdAndArgs string, config config.Config) error
	// WaitReady blocks until the extension with this id has been
	// registered (Run called) and completed its protocol handshake
	// (commands/aliases/keybindings registered), the extension has
	// failed/exited, the runner is closed, or ctx is cancelled.
	// Registration may happen asynchronously after WaitReady is
	// called, so an id that is not yet known is not an error: the call
	// waits for it to appear. It returns nil on a successful handshake,
	// the terminal extension error on failure/exit, and ctx.Err() on
	// cancellation.
	WaitReady(ctx context.Context, id string) error
	// SignalReload tells every running extension process that the config
	// it fetches changed, so it can fetch it again, and returns how many
	// it told. On Unix the process gets SIGUSR2, which Go programs ignore
	// unless they handle it; elsewhere nothing is sent and it returns 0.
	SignalReload() int
}
