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

package standard

import (
	"runtime"

	"github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/text"
)

// standardConfig holds configuration for Editor.
type standardConfig struct {
	tabspaces          int
	indentTabspaces    int
	indents            text.IndentConfig
	indentRune         rune
	ruler              int
	attr               term.Attributes
	search             SearchConfig
	comments           text.CommentConfig
	registry           text.WorkspaceCommandRegistry
	wrap               bool
	enableInitialFolds bool
	enableAuxBar       bool
	auxBarConfig       text.AuxBarConfig
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
	hostMeta           bool
}

// SearchWindowManager manages the floating window used by standard search.
type SearchWindowManager interface {
	Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error)
	CloseWindow(browserapi.Window) error
}

// SearchConfig configures standard-editor find and replace.
type SearchConfig struct {
	WindowManager    SearchWindowManager
	FindKey          term.KeyComb
	ReplaceKey       term.KeyComb
	Attr             term.Attributes
	InputAttr        term.Attributes
	PlaceholderAttr  term.Attributes
	FrameAttr        term.Attributes
	FocusFrameAttr   term.Attributes
	ButtonAttr       term.Attributes
	ButtonHoverAttr  term.Attributes
	MatchAttr        term.Attributes
	CurrentMatchAttr term.Attributes
	StatusAttr       term.Attributes
}

type statusBar interface {
	SetStatus(string, term.Attributes)
}

// MacroRecorder is a cross-editor recorder used to expose standard macro
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

// defaultConfig is a sane configuration defaults for standard handler.
func defaultConfig() standardConfig {
	return standardConfig{
		tabspaces:  component.DefaultTabspaces,
		indentRune: text.IndentRuneTab,
		ruler:      90,
		search: SearchConfig{
			FindKey:    term.KeyComb{Mod: term.ModMeta, Ch: 'f'},
			ReplaceKey: term.KeyComb{Mod: term.ModMeta, Ch: 'r'},
			MatchAttr: term.Attributes{
				Attrs: term.AttrReverse,
			},
			FocusFrameAttr:  term.Attributes{Fg: term.ColorSilver},
			ButtonAttr:      term.Attributes{Bg: term.ColorGray},
			ButtonHoverAttr: term.Attributes{Bg: term.ColorBlue},
		},
		commandBar: true,
		clipboard:  clipboard.NewInMemory(),
		scheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
		notifications:     nopNotifications{},
		cursorCorrections: true,
		hostMeta:          defaultHostMetaChords,
	}
}

// defaultHostMetaChords reports whether the host keeps editing chords on
// Command. Linux desktops own Super, so there they move to Ctrl.
var defaultHostMetaChords = runtime.GOOS == "darwin"

// Option represents a Editor configuration option.
type Option func(*standardConfig)

// WithSearchConfig configures floating standard-editor search.
func WithSearchConfig(search SearchConfig) Option {
	if search.WindowManager == nil {
		panic("standard: SearchConfig.WindowManager must not be nil")
	}
	return func(cfg *standardConfig) {
		if search.FindKey == (term.KeyComb{}) {
			search.FindKey = cfg.search.FindKey
		}
		if search.ReplaceKey == (term.KeyComb{}) {
			search.ReplaceKey = cfg.search.ReplaceKey
		}
		cfg.search = search
	}
}

// WithResAttr sets the search result cell attributes to be rendered.
// Deprecated: use WithSearchConfig.
func WithResAttr(attr term.Attributes) Option {
	return func(cfg *standardConfig) {
		cfg.search.MatchAttr = attr
	}
}

// WithBarAttr sets the incremental-find status attributes.
// Deprecated: use WithSearchConfig.
func WithBarAttr(attr term.Attributes) Option {
	return func(cfg *standardConfig) {
		cfg.search.StatusAttr = attr
	}
}

// WithTabspaces sets the tabspaces value.
func WithTabspaces(tabspaces int) Option {
	return func(cfg *standardConfig) {
		cfg.tabspaces = tabspaces
	}
}

// WithIndents sets language-specific indent material configuration.
func WithIndents(indents text.IndentConfig) Option {
	return func(cfg *standardConfig) {
		cfg.indents = indents
	}
}

// WithRuler sets the ruler column used by paragraph reflow commands.
func WithRuler(ruler int) Option {
	return func(cfg *standardConfig) {
		cfg.ruler = ruler
	}
}

// WithWorkspaceCommandRegistry sets the command registry to register workspace-level
// commands.
func WithWorkspaceCommandRegistry(
	cwd workspaceapi.URI, registry text.WorkspaceCommandRegistry,
) Option {
	return func(cfg *standardConfig) {
		cfg.registry = registry
		cfg.workspace = cwd
	}
}

// WithScheduleNextTick defines the function to schedule and serializes asynchronous work.
func WithScheduleNextTick(fn func(func()) bool) Option {
	return func(cfg *standardConfig) {
		cfg.scheduleNextTick = fn
	}
}

// WithNotifications defines the notifications mechanism to use by Editor.
func WithNotifications(noti browserapi.Notifications) Option {
	return func(cfg *standardConfig) {
		cfg.notifications = noti
	}
}

// WithMacroRecorder sets the macro recorder used by standard macro controls.
func WithMacroRecorder(recorder MacroRecorder) Option {
	return func(cfg *standardConfig) {
		cfg.macroRecorder = recorder
	}
}

// WithMacroPlayer sets the macro player used by standard macro controls.
func WithMacroPlayer(player MacroPlayer) Option {
	return func(cfg *standardConfig) {
		cfg.macroPlayer = player
	}
}

// WithAutoCenter determines whether the editor should automatically
// center the cursor after SetCursorAtScroll.
func WithAutoCenter(enabled bool) Option {
	return func(cfg *standardConfig) {
		cfg.autoCenter = enabled
	}
}

// WithAutoPair determines whether the standard editor should use cursor-level
// delimiter auto-pair behavior while inserting text.
func WithAutoPair(enabled bool) Option {
	return func(cfg *standardConfig) {
		cfg.autoPair = enabled
	}
}

// WithAuxiliaryBar determines whether to draw an auxiliary bar on the left or not.
func WithAuxiliaryBar(enabled bool, config text.AuxBarConfig) Option {
	return func(cfg *standardConfig) {
		cfg.enableAuxBar = enabled
		cfg.auxBarConfig = config
	}
}

// WithIconsBar determines whether to install the icons bar.
func WithIconsBar(enabled bool, config text.IconsBarConfig) Option {
	return func(cfg *standardConfig) {
		cfg.enableIconsBar = enabled
		cfg.iconsBarConfig = config
	}
}

// WithGitIcons determines whether the icons bar should populate git diff icons.
func WithGitIcons(enabled bool) Option {
	return func(cfg *standardConfig) {
		cfg.enableGitIcons = enabled
	}
}

// WithGitBar determines whether to install a git-backed icons bar.
// Deprecated: use WithIconsBar + WithGitIcons.
func WithGitBar(enabled bool, config text.IconsBarConfig) Option {
	return func(cfg *standardConfig) {
		cfg.enableIconsBar = enabled
		cfg.enableGitIcons = enabled
		cfg.iconsBarConfig = config
	}
}

// WithCommandBar enables or disables the command bar.
func WithCommandBar(enabled bool) Option {
	return func(cfg *standardConfig) {
		cfg.commandBar = enabled
	}
}

// WithAttr sets the default cell attributes to be rendered.
func WithAttr(attr term.Attributes) Option {
	return func(cfg *standardConfig) {
		cfg.attr = attr
	}
}

// WithClipboard sets the editor.Clipboard implementation to use.
func WithClipboard(clip clipboard.Register) Option {
	return func(cfg *standardConfig) {
		cfg.clipboard = clip
	}
}

// WithComments sets language-specific comment configuration.
func WithComments(comments text.CommentConfig) Option {
	return func(cfg *standardConfig) {
		cfg.comments = comments
	}
}

// WithWrap enables or disables word wrapping mode.
func WithWrap(wrap bool) Option {
	return func(cfg *standardConfig) {
		cfg.wrap = wrap
	}
}

// WithCursorCorrections enables or disables cursor out-of-bounds corrections.
// Corrections are enabled by default.
func WithCursorCorrections(enabled bool) Option {
	return func(cfg *standardConfig) {
		cfg.cursorCorrections = enabled
	}
}

// WithHostMetaChords selects the platform chord layout. When enabled, as on
// macOS, editing chords use Command (<meta>) and Ctrl+Alt. When disabled, as
// on Linux, where the desktop owns Super and Rune's own commands use
// Ctrl+Alt, the editor ignores every chord carrying <meta> or Ctrl+Alt and
// the chords that exist only there move to Ctrl, Ctrl+Shift and Alt:
//
//	<meta-backspace>  <ctrl-shift-backspace>  delete to line start
//	<meta-delete>     <ctrl-shift-delete>     delete to line end
//	<meta-u>          <ctrl-u>                undo cursor move
//	<shift-meta-u>    <ctrl-shift-u>          redo cursor move
//	<meta-l>          <ctrl-shift-l>          select line
//	<shift-meta-j>    <ctrl-shift-i>          select indentation level
//	<shift-meta-v>    <ctrl-shift-v>          paste and reindent
//	<meta-k> prefix   <ctrl-k> prefix         second key keeps <ctrl> held;
//	                                          <ctrl-k><ctrl-k> cuts to line end
//	<alt-meta-v>      <ctrl-k><ctrl-v>        paste from history
//	<alt-meta-/>      <ctrl-shift-/>          toggle block comment
//	<alt-meta-q>      <ctrl-shift-g>          wrap paragraph
//	<ctrl-meta-d>     <ctrl-shift-d>          select previous occurrence
//	<ctrl-alt-up>     <alt-pgup>              scroll up
//	<ctrl-alt-down>   <alt-pgdn>              scroll down
//	<ctrl-alt-h>      <ctrl-shift-h>          hide selection
//	<ctrl-alt-v>      <ctrl-shift-r>          reveal hidden lines
//
// The default follows the host platform.
func WithHostMetaChords(enabled bool) Option {
	return func(cfg *standardConfig) {
		cfg.hostMeta = enabled
	}
}

// WithHideInitialFolds determines whether to hide the initial folds
// determined by the language query.
func WithHideInitialFolds(enabled bool) Option {
	return func(cfg *standardConfig) {
		cfg.enableInitialFolds = enabled
	}
}

// WithStatusBarConfig configures the status bar.
func WithStatusBarConfig(enabled bool, config text.StatusBarConfig) Option {
	return func(cfg *standardConfig) {
		cfg.statusBarConfig = config
		cfg.statusBarEnabled = enabled
	}
}

type nopBar struct {
}

func (n nopBar) SetStatus(status string, _ term.Attributes) {
	logrus.Infof("standard handler status: %s", status)
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
