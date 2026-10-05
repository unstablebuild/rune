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

package workspacessh

import "context"

// NotificationLevel mirrors the levels supported by the IDE notifications
// surface. Defined here so the workspacessh package does not depend on the
// IDE notifications package directly.
type NotificationLevel int

const (
	// NotificationInfo is an informational notification.
	NotificationInfo NotificationLevel = iota
	// NotificationWarning is a warning notification.
	NotificationWarning
	// NotificationError is an error notification.
	NotificationError
)

// UI is the surface workspacessh uses to interact with the user when
// authenticating against a remote host. The IDE provides a Browser-backed
// implementation; tests provide their own (typically recording) impl.
//
// All methods may be invoked from goroutines that are not the IDE's event
// loop: implementations are responsible for marshalling work onto the
// appropriate goroutine if required.
type UI interface {
	// PromptSecret displays a redacted single-line input with the given label
	// and returns the typed value. Returns context.Canceled if the user
	// dismisses the prompt.
	PromptSecret(ctx context.Context, label string) (string, error)

	// PromptText displays an unredacted single-line input. defaultValue may be
	// empty.
	PromptText(ctx context.Context, label, defaultValue string) (string, error)

	// PromptChoice displays a multi-option prompt and returns the selected
	// index, or context.Canceled if dismissed.
	PromptChoice(ctx context.Context, message string, options []string) (int, error)

	// Notify shows a non-blocking notification.
	Notify(level NotificationLevel, msg string)
}
