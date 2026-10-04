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

package term

import (
	"context"
)

// key is an unexported type for keys defined in this package.
// This prevents collisions with keys defined in other packages.
type ctxKey int

// pKey is the key for sync.Payload values in Contexts. It is
// unexported; clients use workspace.ContextWithPayload and
// PayloadFromContext instead of using this key directly.
var pKey ctxKey

// scKey is the key for SubCellFraction values in Contexts.
var scKey ctxKey = 1

// ContextWithPayload returns a new Context that holds locker.
//
// Deprecated: use term.Event.Context to pass a context.
func ContextWithPayload(ctx context.Context, payload []byte) context.Context {
	return context.WithValue(ctx, pKey, payload)
}

// PayloadFromContext returns the payload value stored in ctx, if any.
//
// Deprecated: use term.Event.Context to pass a context.
func PayloadFromContext(ctx context.Context) ([]byte, bool) {
	locker, ok := ctx.Value(pKey).([]byte)
	return locker, ok
}

// SubCellFraction is a pointer's fractional position inside the cell that
// Event.MouseX/MouseY report, in [0,1) on each axis. The cell fields alone
// lose where inside the cell the pointer is, which consumers that model
// positions as cell boundaries (e.g. drag selection) need to snap to the
// nearer edge. The fraction is cell-translation invariant, so it stays
// valid as the event's coordinates are shifted into component-local space.
type SubCellFraction struct {
	X, Y float64
}

// ContextWithSubCellFraction returns a Context that holds f.
func ContextWithSubCellFraction(ctx context.Context, f SubCellFraction) context.Context {
	return context.WithValue(ctx, scKey, f)
}

// SubCellFractionFromContext returns the fraction stored in ctx, if any.
func SubCellFractionFromContext(ctx context.Context) (SubCellFraction, bool) {
	if ctx == nil {
		return SubCellFraction{}, false
	}
	f, ok := ctx.Value(scKey).(SubCellFraction)
	return f, ok
}
