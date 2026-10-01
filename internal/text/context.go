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

package text

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/vctrl"
)

type ctxKey int

const (
	barsKey ctxKey = iota
	noAutoCenterKey
)

// BarOptions selects which auxiliary bars an Edit call installs and,
// when the caller renders a synthetic resource, what the status bar
// should resolve git state against.
type BarOptions struct {
	DisableAuxBar   bool
	DisableIconsBar bool
	// StatusBar, when set, overrides the editor's configured workspace
	// and git service for this Edit only.
	StatusBar *StatusBarOverride
}

// StatusBarOverride redirects the status bar at a resource the editor's
// own configuration cannot resolve.
type StatusBarOverride struct {
	Workspace  workspaceapi.URI
	GitService vctrl.Service
}

// WithBars marks ctx as requesting auxiliary bars under opts.
func WithBars(ctx context.Context, opts BarOptions) context.Context {
	return context.WithValue(ctx, barsKey, opts)
}

// BarsFromContext returns the options WithBars stored and whether the
// context asked for bars at all. text.Editor implementations call it to
// decide whether to wrap the returned text.Handler with status / icons
// / aux bars.
func BarsFromContext(ctx context.Context) (BarOptions, bool) {
	v, ok := ctx.Value(barsKey).(BarOptions)
	return v, ok
}

// WithoutAutoCenter marks ctx as asking Edit for a handler whose
// SetCursorAtScroll scrolls only as far as needed to show the cursor,
// even when the editor is configured to center it. Callers restoring
// a view (a scroll offset and a cursor within it) need it, since
// centering moves the view and seeking it back moves the cursor.
func WithoutAutoCenter(ctx context.Context) context.Context {
	return context.WithValue(ctx, noAutoCenterKey, true)
}

// AutoCenterDisabled reports whether ctx was marked by
// WithoutAutoCenter. text.Editor implementations call it from Edit.
func AutoCenterDisabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(noAutoCenterKey).(bool)
	return disabled
}
