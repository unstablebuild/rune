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

package idepkg

import (
	"github.com/unstablebuild/rune-go-sdk/component"
)

// Option configures a Manager.
type Option func(*Manager)

// WithFrameCharSet sets the component.FrameCharSet to use for prompts.
func WithFrameCharSet(fcs component.FrameCharSet) Option {
	return func(m *Manager) {
		m.frameCharSet = fcs
	}
}

// WithEditorMode sets the resolved editor mode ("vim", "helix",
// "standard", "emacs") exposed to package config.star scripts as the
// RUNE_EDITOR_MODE predeclared global. Empty values are not forwarded.
func WithEditorMode(mode string) Option {
	return func(m *Manager) {
		m.editorMode = mode
	}
}

// WithAfterConfigWrite installs a hook called with the package's ID after
// the config a package contributes changed: its config was written to the
// etc directory and included from the user config on install or use, or
// removed from both on uninstall. The hook reloads the config to see the
// change; the Manager does not know what the keys mean.
func WithAfterConfigWrite(hook func(pkgID string)) Option {
	return func(m *Manager) {
		m.afterConfigWrite = hook
	}
}

// WithAfterInstall installs a hook called with the package's ID once a
// version of it is installed or put in use and its files are in place.
// The hook runs on the goroutine that installed the package.
func WithAfterInstall(hook func(pkgID string)) Option {
	return func(m *Manager) {
		m.afterInstall = hook
	}
}
