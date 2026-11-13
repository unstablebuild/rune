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

package emacs

import (
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

// emacsConfig holds configuration for Editor.
type emacsConfig struct {
	tabspaces          int
	indentTabspaces    int
	indents            text.IndentConfig
	indentRune         rune
	ruler              int
	attr               term.Attributes
	barAttr            term.Attributes
	messageBarLayout   handler.LessMessageLayout
	resAttr            term.Attributes
	comments           text.CommentConfig
	registry           text.WorkspaceCommandRegistry
	wrap               bool
	enableInitialFolds bool
	enableAuxBar       bool
	auxBarConfig       text.AuxBarConfig
	hatch              component.HatchStyle
	enableIconsBar     bool
	enableGitIcons     bool
	commandBar         bool
	iconsBarConfig     text.IconsBarConfig
	workspace          workspaceapi.URI
	clipboard          clipboard.Register
	notifications      browserapi.Notifications
	macroRecorder      MacroRecorder
	macroPlayer        MacroPlayer
	autoCenter         bool
	autoPair           bool
	cursorCorrections  bool
	statusBarConfig    text.StatusBarConfig
	statusBarEnabled   bool
	scheduleNextTick   func(fn func()) bool
}

type statusBar interface {
	SetStatus(string, term.Attributes)
}

// MacroRecorder is a cross-editor recorder used to expose emacs macro
// controls without owning the underlying recording implementation.
type MacroRecorder interface {
	Start(registerID string)
	Stop()
	IsRecording() bool
}

// MacroPlayer triggers macro playback from a clipboard register.
// The count parameter specifies how many times to replay the register.
type MacroPlayer interface {
	Play(registerID string, count int) error
	IsPlaying() bool
}

// defaultConfig is a sane configuration defaults for emacs handler.
func defaultConfig() emacsConfig {
	return emacsConfig{
		tabspaces:  component.DefaultTabspaces,
		indentRune: text.IndentRuneTab,
		ruler:      90,
		resAttr: term.Attributes{
			Attrs: term.AttrReverse,
		},
		commandBar: true,
		clipboard:  clipboard.NewInMemory(),
		scheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
		notifications:     nopNotifications{},
		cursorCorrections: true,
	}
}

// Option represents a Editor configuration option.
type Option func(*emacsConfig)

// WithResAttr sets the search result cell attributes to be rendered.
func WithResAttr(attr term.Attributes) Option {
	return func(cfg *emacsConfig) {
		cfg.resAttr = attr
	}
}

// WithBarAttr sets the echo-area bar attributes.
func WithBarAttr(attr term.Attributes) Option {
	return func(cfg *emacsConfig) {
		cfg.barAttr = attr
	}
}

// WithMessageBarLayout configures the Emacs echo-area message layout.
func WithMessageBarLayout(layout handler.LessMessageLayout) Option {
	return func(cfg *emacsConfig) {
		cfg.messageBarLayout = layout
	}
}

// WithTabspaces sets the tabspaces value.
func WithTabspaces(tabspaces int) Option {
	return func(cfg *emacsConfig) {
		cfg.tabspaces = tabspaces
	}
}

// WithIndents sets language-specific indent material configuration.
func WithIndents(indents text.IndentConfig) Option {
	return func(cfg *emacsConfig) {
		cfg.indents = indents
	}
}

// WithRuler sets the ruler column used by paragraph reflow commands.
func WithRuler(ruler int) Option {
	return func(cfg *emacsConfig) {
		cfg.ruler = ruler
	}
}

// WithWorkspaceCommandRegistry sets the command registry to register workspace-level
// commands.
func WithWorkspaceCommandRegistry(
	cwd workspaceapi.URI, registry text.WorkspaceCommandRegistry,
) Option {
	return func(cfg *emacsConfig) {
		cfg.registry = registry
		cfg.workspace = cwd
	}
}

// WithScheduleNextTick defines the function to schedule and serializes asynchronous work.
func WithScheduleNextTick(fn func(func()) bool) Option {
	return func(cfg *emacsConfig) {
		cfg.scheduleNextTick = fn
	}
}

// WithNotifications defines the notifications mechanism to use by Editor.
func WithNotifications(noti browserapi.Notifications) Option {
	return func(cfg *emacsConfig) {
		cfg.notifications = noti
	}
}

// WithMacroRecorder sets the macro recorder used by emacs macro controls.
func WithMacroRecorder(recorder MacroRecorder) Option {
	return func(cfg *emacsConfig) {
		cfg.macroRecorder = recorder
	}
}

// WithMacroPlayer sets the macro player used by emacs macro controls.
func WithMacroPlayer(player MacroPlayer) Option {
	return func(cfg *emacsConfig) {
		cfg.macroPlayer = player
	}
}

// WithAutoCenter determines whether the editor should automatically
// center the cursor after SetCursorAtScroll.
func WithAutoCenter(enabled bool) Option {
	return func(cfg *emacsConfig) {
		cfg.autoCenter = enabled
	}
}

// WithAutoPair determines whether the emacs editor should use cursor-level
// delimiter auto-pair behavior while inserting text.
func WithAutoPair(enabled bool) Option {
	return func(cfg *emacsConfig) {
		cfg.autoPair = enabled
	}
}

// WithAuxiliaryBar determines whether to draw an auxiliary bar on the left or not.
func WithAuxiliaryBar(enabled bool, config text.AuxBarConfig) Option {
	return func(cfg *emacsConfig) {
		cfg.enableAuxBar = enabled
		cfg.auxBarConfig = config
	}
}

// WithIconsBar determines whether to install the icons bar.
func WithIconsBar(enabled bool, config text.IconsBarConfig) Option {
	return func(cfg *emacsConfig) {
		cfg.enableIconsBar = enabled
		cfg.iconsBarConfig = config
	}
}

// WithHatch sets the stripes drawn over the rows past the end of a file.
func WithHatch(style component.HatchStyle) Option {
	return func(cfg *emacsConfig) {
		cfg.hatch = style
	}
}

// WithGitIcons determines whether the icons bar should populate git diff icons.
func WithGitIcons(enabled bool) Option {
	return func(cfg *emacsConfig) {
		cfg.enableGitIcons = enabled
	}
}

// WithGitBar determines whether to install a git-backed icons bar.
// Deprecated: use WithIconsBar + WithGitIcons.
func WithGitBar(enabled bool, config text.IconsBarConfig) Option {
	return func(cfg *emacsConfig) {
		cfg.enableIconsBar = enabled
		cfg.enableGitIcons = enabled
		cfg.iconsBarConfig = config
	}
}

// WithCommandBar enables or disables the command bar.
func WithCommandBar(enabled bool) Option {
	return func(cfg *emacsConfig) {
		cfg.commandBar = enabled
	}
}

// WithAttr sets the default cell attributes to be rendered.
func WithAttr(attr term.Attributes) Option {
	return func(cfg *emacsConfig) {
		cfg.attr = attr
	}
}

// WithClipboard sets the editor.Clipboard implementation to use.
func WithClipboard(clip clipboard.Register) Option {
	return func(cfg *emacsConfig) {
		cfg.clipboard = clip
	}
}

// WithComments sets language-specific comment configuration.
func WithComments(comments text.CommentConfig) Option {
	return func(cfg *emacsConfig) {
		cfg.comments = comments
	}
}

// WithWrap enables or disables word wrapping mode.
func WithWrap(wrap bool) Option {
	return func(cfg *emacsConfig) {
		cfg.wrap = wrap
	}
}

// WithCursorCorrections enables or disables cursor out-of-bounds corrections.
// Corrections are enabled by default.
func WithCursorCorrections(enabled bool) Option {
	return func(cfg *emacsConfig) {
		cfg.cursorCorrections = enabled
	}
}

// WithHideInitialFolds determines whether to hide the initial folds
// determined by the language query.
func WithHideInitialFolds(enabled bool) Option {
	return func(cfg *emacsConfig) {
		cfg.enableInitialFolds = enabled
	}
}

// WithStatusBarConfig configures the status bar.
func WithStatusBarConfig(enabled bool, config text.StatusBarConfig) Option {
	return func(cfg *emacsConfig) {
		cfg.statusBarConfig = config
		cfg.statusBarEnabled = enabled
	}
}

type nopBar struct {
}

func (n nopBar) SetStatus(status string, _ term.Attributes) {
}

func (n nopBar) ShowBar(bool) {
}

type nopNotifications struct {
}

func (nopNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}

func (nopNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}

func (n nopNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}
