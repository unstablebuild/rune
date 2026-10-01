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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

// TestOptions pins that every option reaches the config it configures.
func TestOptions(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)
	indents := text.IndentConfig{"go": text.IndentRuneTab}
	comments := text.CommentConfig{"go": {Line: []string{"//"}}}
	attr := term.Attributes{Fg: term.ColorRed}
	clip := clipboard.NewInMemory()
	noti := nopNotifications{}
	rec := new(fakeMacroRecorder)
	player := new(fakeMacroPlayer)

	for _, tc := range []struct {
		name string
		opt  Option
		want func(*testing.T, helixConfig)
	}{
		{name: "WithTabspaces", opt: WithTabspaces(7),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, 7, c.tabspaces) }},
		{name: "WithIndents", opt: WithIndents(indents),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, indents, c.indents) }},
		{name: "WithRuler", opt: WithRuler(42),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, 42, c.ruler) }},
		{name: "WithResAttr", opt: WithResAttr(attr),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, attr, c.resAttr) }},
		{name: "WithMessageBarLayout",
			opt: WithMessageBarLayout(handler.LessMessageLayout{Template: "%s"}),
			want: func(t *testing.T, c helixConfig) {
				assert.Equal(t, "%s", c.messageBarLayout.Template)
			}},
		{name: "WithBarAttr", opt: WithBarAttr(attr),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, attr, c.barAttr) }},
		{name: "WithNotifications", opt: WithNotifications(noti),
			want: func(t *testing.T, c helixConfig) {
				assert.Equal(t, browserapi.Notifications(noti), c.notifications)
			}},
		{name: "WithMacroRecorder", opt: WithMacroRecorder(rec),
			want: func(t *testing.T, c helixConfig) {
				assert.Equal(t, MacroRecorder(rec), c.macroRecorder)
			}},
		{name: "WithMacroPlayer", opt: WithMacroPlayer(player),
			want: func(t *testing.T, c helixConfig) {
				assert.Equal(t, MacroPlayer(player), c.macroPlayer)
			}},
		{name: "WithWorkspaceCommandRegistry",
			opt:  WithWorkspaceCommandRegistry(cwd, nil),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, cwd, c.workspace) }},
		{name: "WithScheduleNextTick", opt: WithScheduleNextTick(func(func()) bool { return false }),
			want: func(t *testing.T, c helixConfig) {
				assert.False(t, c.scheduleNextTick(func() {}))
			}},
		{name: "WithAuxiliaryBar", opt: WithAuxiliaryBar(true, text.AuxBarConfig{}),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.enableAuxBar) }},
		{name: "WithStatusBarConfig", opt: WithStatusBarConfig(true, text.StatusBarConfig{}),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.statusBarEnabled) }},
		{name: "WithIconsBar", opt: WithIconsBar(true, text.IconsBarConfig{}),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.enableIconsBar) }},
		{name: "WithGitIcons", opt: WithGitIcons(true),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.enableGitIcons) }},
		{name: "WithAttr", opt: WithAttr(attr),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, attr, c.attr) }},
		{name: "WithClipboard", opt: WithClipboard(clip),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, clip, c.clipboard) }},
		{name: "WithComments", opt: WithComments(comments),
			want: func(t *testing.T, c helixConfig) { assert.Equal(t, comments, c.comments) }},
		{name: "WithWrap", opt: WithWrap(true),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.wrap) }},
		{name: "WithCursorCorrections", opt: WithCursorCorrections(false),
			want: func(t *testing.T, c helixConfig) { assert.False(t, c.cursorCorrections) }},
		{name: "WithAutoCenter", opt: WithAutoCenter(true),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.autoCenter) }},
		{name: "WithAutoPair", opt: WithAutoPair(true),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.autoPair) }},
		{name: "WithHideInitialFolds", opt: WithHideInitialFolds(true),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.enableInitialFolds) }},
		{name: "WithSearch", opt: WithSearch(false),
			want: func(t *testing.T, c helixConfig) { assert.True(t, c.disableSearch) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultHelixConfig()
			tc.opt(&cfg)
			tc.want(t, cfg)
		})
	}
}

// TestNopHelpers pins the inert defaults so a handler built without an
// IDE around it never dereferences nil.
func TestNopHelpers(t *testing.T) {
	var bar nopBar
	bar.SetStatus("x", term.Attributes{})
	bar.ShowBar(true)

	var noti nopNotifications
	_, err := noti.Notify(browserapi.LevelError, "a")
	require.NoError(t, err)
	_, err = noti.NotifyOnce(browserapi.LevelError, "a")
	require.NoError(t, err)
	require.NoError(t, noti.UpdateNotificationProgress("id", "msg", 1, 2))
}

// TestConfiguredBehaviour drives the options that change how keys act.
func TestConfiguredBehaviour(t *testing.T) {
	t.Run("auto pair closes brackets", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{}, WithAutoPair(true))
		send(t, hx, keys("i(")...)
		assert.Equal(t, "()", buf.String())
	})

	t.Run("auto pair backspace removes both halves", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{}, WithAutoPair(true))
		send(t, hx, keys("i(")...)
		send(t, hx, namedKey(term.KeyBackspace))
		assert.Equal(t, "", buf.String())
	})

	t.Run("without auto pair nothing is closed", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("i(")...)
		assert.Equal(t, "(", buf.String())
	})

	t.Run("auto pair splits on enter", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{}, WithAutoPair(true))
		send(t, hx, keys("i{")...)
		send(t, hx, namedKey(term.KeyEnter))
		assert.Contains(t, buf.String(), "{")
		assert.Contains(t, buf.String(), "}")
	})

	t.Run("auto center recenters on an explicit jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("line\n", 200), term.Coordinates{},
			WithAutoCenter(true))
		require.True(t, hx.SetCursorAtScroll(term.Coordinates{Y: 150}))
		assert.Greater(t, hx.SeekOffset(), 0)
	})

	t.Run("cursor corrections can be disabled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcd\nx", term.Coordinates{X: 3},
			WithCursorCorrections(false))
		send(t, hx, key('j'))
		assert.Equal(t, 1, hx.CursorAtScroll().Y)
	})

	t.Run("wrap is applied at construction", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithWrap(true))
		assert.True(t, hx.less.Scroll().Wrap)
	})

	t.Run("tabspaces reach the scroll", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\tb", term.Coordinates{}, WithTabspaces(8))
		send(t, hx, key('l'))
		assert.Equal(t, "\t", sel(t, hx))
	})
}
