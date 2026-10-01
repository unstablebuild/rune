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

package helix

import (
	"github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

// helixConfig holds configuration for Helix.
type helixConfig struct {
	attr               term.Attributes
	resAttr            term.Attributes
	barAttr            term.Attributes
	messageBarLayout   handler.LessMessageLayout
	comments           text.CommentConfig
	clipboard          clipboard.Register
	tabspaces          int
	indentTabspaces    int
	indents            text.IndentConfig
	indentRune         rune
	ruler              int
	scheduleNextTick   func(func()) bool
	defaultRegister    string
	registry           text.WorkspaceCommandRegistry
	wrap               bool
	cursorCorrections  bool
	autoCenter         bool
	autoPair           bool
	enableInitialFolds bool
	disableSearch      bool
	enableAuxBar       bool
	auxBarConfig       text.AuxBarConfig
	statusBarConfig    text.StatusBarConfig
	statusBarEnabled   bool
	iconsBarConfig     text.IconsBarConfig
	workspace          workspaceapi.URI
	enableIconsBar     bool
	enableGitIcons     bool
	notifications      browserapi.Notifications
	macroRecorder      MacroRecorder
	macroPlayer        MacroPlayer
}

// MacroRecorder is a cross-editor recorder used to expose Helix-style macro
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

// defaultHelixConfig is a sane set of configuration defaults for helixHandlerImpl.
func defaultHelixConfig() helixConfig {
	return helixConfig{
		tabspaces:  component.DefaultTabspaces,
		indentRune: text.IndentRuneTab,
		ruler:      90,
		resAttr: term.Attributes{
			Attrs: term.AttrReverse,
		},
		clipboard: clipboard.NewInMemory(),
		scheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
		defaultRegister:   clipboard.DefaultRegisterID,
		notifications:     nopNotifications{},
		cursorCorrections: true,
	}
}

// Option represents a Helix handler configuration option.
type Option func(*helixConfig)

// WithTabspaces sets the tabspaces value.
func WithTabspaces(tabspaces int) Option {
	return func(cfg *helixConfig) {
		cfg.tabspaces = tabspaces
	}
}

// WithIndents sets language-specific indent material configuration.
func WithIndents(indents text.IndentConfig) Option {
	return func(cfg *helixConfig) {
		cfg.indents = indents
	}
}

// WithRuler sets the ruler column used by paragraph reflow commands.
func WithRuler(ruler int) Option {
	return func(cfg *helixConfig) {
		cfg.ruler = ruler
	}
}

// WithResAttr sets the search result cell attributes to be rendered.
func WithResAttr(attr term.Attributes) Option {
	return func(cfg *helixConfig) {
		cfg.resAttr = attr
	}
}

// WithMessageBarLayout configures the layout of the superimposed message bar.
func WithMessageBarLayout(layout handler.LessMessageLayout) Option {
	return func(cfg *helixConfig) {
		cfg.messageBarLayout = layout
	}
}

// WithBarAttr sets the base attributes of the superimposed message bar.
func WithBarAttr(attr term.Attributes) Option {
	return func(cfg *helixConfig) {
		cfg.barAttr = attr
	}
}

// WithNotifications defines the notifications mechanism to use by Editor.
func WithNotifications(noti browserapi.Notifications) Option {
	return func(cfg *helixConfig) {
		cfg.notifications = noti
	}
}

// WithMacroRecorder installs a cross-editor macro recorder for Helix's Q flow.
func WithMacroRecorder(recorder MacroRecorder) Option {
	return func(cfg *helixConfig) {
		cfg.macroRecorder = recorder
	}
}

// WithMacroPlayer installs a cross-editor macro player for Helix's q flow.
func WithMacroPlayer(player MacroPlayer) Option {
	return func(cfg *helixConfig) {
		cfg.macroPlayer = player
	}
}

// WithWorkspaceCommandRegistry sets the command registry to register
// workspace-level commands.
func WithWorkspaceCommandRegistry(
	cwd workspaceapi.URI, registry text.WorkspaceCommandRegistry,
) Option {
	return func(cfg *helixConfig) {
		cfg.registry = registry
		cfg.workspace = cwd
	}
}

// WithScheduleNextTick defines the function to schedule and serialize
// asynchronous work.
func WithScheduleNextTick(fn func(func()) bool) Option {
	return func(cfg *helixConfig) {
		cfg.scheduleNextTick = fn
	}
}

// WithAuxiliaryBar determines whether to draw an auxiliary bar on the left or not.
func WithAuxiliaryBar(enabled bool, config text.AuxBarConfig) Option {
	return func(cfg *helixConfig) {
		cfg.enableAuxBar = enabled
		cfg.auxBarConfig = config
	}
}

// WithStatusBarConfig configures the status bar.
func WithStatusBarConfig(enabled bool, config text.StatusBarConfig) Option {
	return func(cfg *helixConfig) {
		cfg.statusBarConfig = config
		cfg.statusBarEnabled = enabled
	}
}

// WithIconsBar determines whether to install the icons bar.
func WithIconsBar(enabled bool, config text.IconsBarConfig) Option {
	return func(cfg *helixConfig) {
		cfg.enableIconsBar = enabled
		cfg.iconsBarConfig = config
	}
}

// WithGitIcons determines whether the icons bar should populate git diff icons.
func WithGitIcons(enabled bool) Option {
	return func(cfg *helixConfig) {
		cfg.enableGitIcons = enabled
	}
}

// WithAttr sets the default cell attributes to be rendered.
func WithAttr(attr term.Attributes) Option {
	return func(cfg *helixConfig) {
		cfg.attr = attr
	}
}

// WithClipboard sets the clipboard.Register implementation to use.
func WithClipboard(clip clipboard.Register) Option {
	return func(cfg *helixConfig) {
		cfg.clipboard = clip
	}
}

// WithComments sets language-specific comment configuration.
func WithComments(comments text.CommentConfig) Option {
	return func(cfg *helixConfig) {
		cfg.comments = comments
	}
}

// WithWrap enables or disables word wrapping mode.
func WithWrap(wrap bool) Option {
	return func(cfg *helixConfig) {
		cfg.wrap = wrap
	}
}

// WithCursorCorrections enables or disables cursor out of bounds corrections.
// By default it's enabled, unless this option is passed; when disabled, clients
// must manage it themselves.
func WithCursorCorrections(enabled bool) Option {
	return func(cfg *helixConfig) {
		cfg.cursorCorrections = enabled
	}
}

// WithAutoCenter determines whether helix should automatically
// center the cursor after SetCursorAtScroll.
func WithAutoCenter(enabled bool) Option {
	return func(cfg *helixConfig) {
		cfg.autoCenter = enabled
	}
}

// WithAutoPair determines whether insert mode should use cursor-level
// delimiter auto-pair behavior while inserting text.
func WithAutoPair(enabled bool) Option {
	return func(cfg *helixConfig) {
		cfg.autoPair = enabled
	}
}

// WithHideInitialFolds determines whether to hide the initial folds
// determined by the language query.
func WithHideInitialFolds(enabled bool) Option {
	return func(cfg *helixConfig) {
		cfg.enableInitialFolds = enabled
	}
}

// WithSearch enables or disables the `/` and `?` search commands. Search is
// enabled by default; disabling it makes those keys no-ops so editors embedded
// in a shell that owns search do not capture them.
func WithSearch(enabled bool) Option {
	return func(cfg *helixConfig) {
		cfg.disableSearch = !enabled
	}
}

type nopBar struct{}

func (n nopBar) SetStatus(status string, _ term.Attributes) {
	logrus.Debugf("helix handler status: %s", status)
}

func (n nopBar) ShowBar(bool) {}

type nopNotifications struct{}

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
