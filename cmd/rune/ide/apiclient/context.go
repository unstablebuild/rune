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

package apiclient

import (
	"context"
	"net/url"
)

// loginURLCtxKey identifies the per-call OAuth URL channel attached
// to a Login context. The channel is the only signal that a browser
// flow is explicitly requested: background callers do not set it, so
// tokenSourceRefresh refuses to launch a browser when it is absent.
type loginURLCtxKey struct{}

func withLoginURLCh(ctx context.Context, ch chan<- *url.URL) context.Context {
	return context.WithValue(ctx, loginURLCtxKey{}, ch)
}

func loginURLChFrom(ctx context.Context) (chan<- *url.URL, bool) {
	ch, ok := ctx.Value(loginURLCtxKey{}).(chan<- *url.URL)
	return ch, ok
}

// devicePromptCtxKey identifies the per-call device prompt channel
// attached to a LoginWithDeviceCode context. Like the URL channel it
// is the only signal that a device-code flow was explicitly requested.
type devicePromptCtxKey struct{}

func withDevicePromptCh(ctx context.Context, ch chan<- DevicePrompt) context.Context {
	return context.WithValue(ctx, devicePromptCtxKey{}, ch)
}

func devicePromptChFrom(ctx context.Context) (chan<- DevicePrompt, bool) {
	ch, ok := ctx.Value(devicePromptCtxKey{}).(chan<- DevicePrompt)
	return ch, ok
}
