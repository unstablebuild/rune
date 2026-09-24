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
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
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

// WithSyntaxParser sets the syntax parser for showing config changes.
func WithSyntaxParser(parser syntaxapi.Parser) Option {
	return func(m *Manager) {
		m.parser = parser
	}
}

// WithEditorMode sets the resolved editor mode ("modal", "helix",
// "standard", "emacs") exposed to package config.star scripts as the
// RUNE_EDITOR_MODE predeclared global. Empty values are not forwarded.
func WithEditorMode(mode string) Option {
	return func(m *Manager) {
		m.editorMode = mode
	}
}

// WithConfigBase sets a provider for the editor's default config tree. It is
// predeclared as `config` when reading the user's .star config so
// overlay-style mutations (config[...] = ...) and Rune's managed block
// resolve without binding `config`. The provider is called per read so it
// reflects the current configuration.
func WithConfigBase(base func() map[string]any) Option {
	return func(m *Manager) {
		m.configBase = base
	}
}

// WithAfterConfigMerge installs a hook invoked after a package config merge is
// written to the user config file. The hook is generic and unaware of GUI or
// environment concerns; callers inspect the applied diff and report back
// whether any live reload happened via ConfigMergeResult. The hook runs for
// both the auto-apply path and the prompt's Allow path, and never for Deny.
func WithAfterConfigMerge(hook func(ConfigMergeEvent) (ConfigMergeResult, error)) Option {
	return func(m *Manager) {
		m.afterConfigMerge = hook
	}
}
