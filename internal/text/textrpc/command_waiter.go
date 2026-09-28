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

package textrpc

import "context"

// Waiter lets a command dispatcher learn when an asynchronously handled
// command finishes. A caller attaches it to the dispatch context with
// ContextWithWaiter; a handler whose HandleCommand returns before the
// command is actually done (e.g. the out-of-process extension command
// stream) reports completion on Ch instead.
//
// Claimed is the contract between such a handler and the caller: the
// handler MUST set Claimed before HandleCommand returns when it takes
// responsibility for delivering the result on Ch. The caller reads
// Claimed only after dispatch returns, so a plain bool is sufficient —
// no synchronization is needed. When Claimed is false the command
// completed synchronously and nothing is sent on Ch.
//
// Ch must be buffered: a claimed result is reported from whichever
// goroutine finished the command, including the event loop, and must
// never block on the caller reading it.
type Waiter struct {
	Ch      chan error
	Claimed bool
}

type waiterKey struct{}

// ContextWithWaiter returns a context carrying w so a command stream can
// report asynchronous completion back to the caller through w.Ch.
func ContextWithWaiter(ctx context.Context, w *Waiter) context.Context {
	return context.WithValue(ctx, waiterKey{}, w)
}

// WaiterFromContext returns the Waiter attached by ContextWithWaiter, if
// any.
func WaiterFromContext(ctx context.Context) (*Waiter, bool) {
	w, ok := ctx.Value(waiterKey{}).(*Waiter)
	return w, ok
}
