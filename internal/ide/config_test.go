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

package ide

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
)

var sampleConfig = `
extensions:
    fuzzy_search:
        path: "/path/extension_fuzzy_search"
        config:
            file:
                command: ag -g ""

log_path: "/tmp/debug.log"
log_level: "trace"
clipboard: memory
default_attr:
    bg: yellow
    fg: "#f2f2f2"

editor:
    ruler: 72
    auto_pair: true
    auto_save: true
    status_bar:
        enabled: true
        background_attr:
            bg: maroon
        layout: '█{{ .Status | bg "red" | fg "black" | bold }}█▓▒░  {{ .Filepath }}   {{ .GitShortRef }}   {{ .GitDiffAdd | fg "green" }}   {{ .GitDiffDel | fg "red" }} {{ .ShiftRight }}{{ .CursorColumn }}:{{ .CursorLine }}  {{ .TotalLines }} lines  {{ .Language | bold }}  '
    aux_bar:
        enabled: true
        icons: true
        folds: true
        lines: relative
        highlight_cursor: false
        git: all
        git_del_inline_attr:
            fg: maroon
            flags: bold
        git_add_inline_attr:
            fg: green
            flags: bold
        git_del_locations_attr:
            bg: maroon
            flags: dim
        git_add_locations_attr:
            bg: green
            flags: dim
        line_number_attr:
            fg: gray
            bg: default
        highlight_cursor_attr:
            fg: white
            bg: gray
            flags: bold
    mode: modal
    modal:
        attr:
            bg: yellow
            fg: "#f2f2f2"
        search_attr:
            bg: red
            fg: "#f0f0f0"
        message_bar:
            attr:
                bg: navy
                fg: silver
            layout: '▓▒░ {{ .Message | bg "navy" | fg "silver" | italic }} '
        debug: true
        wrap: true
    standard:
        attr:
            bg: green
            fg: "#f9f9f9"
        search_attr:
            bg: red
            fg: "#f1f1f1"
        wrap: false
    emacs:
        attr:
            bg: blue
            fg: "#f8f8f8"
        message_bar:
            attr:
                bg: teal
                fg: white
            layout: ' {{ .Message | bg "red" | fg "white" | bold }} █▓▒░'
        search_attr:
            bg: maroon
            fg: "#f7f7f7"
    virtual:
        shell: bash
        editor: vim
        attr:
            bg: red
            fg: "#f0f0f0"
        selection_attr:
            bg: green
            fg: "#f3f3f3"
    autoindent: false
    comments:
        go:
            line:
                - "//"
            block:
                - "/*"
                - "*/"
    highlights:
        function:
            fg: green
            bg: yellow

input_mode:
  - mouse
  - esc

lsp:
    icons:
        error: E
        warning: W
        information: I
        hint: H
        inline: '>'
        escape: '^'
        bounds: B
        nilcheck: '0'
        compiler: C

command:
  show_manual: false
  aliases:
    cherry: bomb
    todo:
      - e file:///tmp/todo.md
      - jenesaisquoi
    error: 1
    parcels:
      commands:
        - Somethinggreater
        - NowIcaresomemore
        - Comingback
      completer: files
    daynight:
      commands: ram
      completer:
        - a
        - B
    dtmf:
      commands: ram
      completer: '! hello'
    editmix:
      commands: e
      completer:
        - '{history}'
        - '{file}'
    static_with_hist:
      commands: m
      completer:
        - '{history}'
        - a
        - B
    bad:
      commands: x
      completer:
        - '{nope}'
  manual_attr:
    fg: black
    bg: yellow
    flags: bold
  key_bindings:
    f: searchfile
    l: searchtext
    <c-x>: closeDoors
    <c-x><c-p>: openAllDoors
    f<c-p>: openDoors small
    <c-x>9:
      - openDoors 1
      - large 2
    <c-x>p:
      - invalid A
      - smtg:
        - else
    <-x>f: invalidMapping

notifications:
    auto_close: 1s
    padding: 1
    progress_bar: false
    progress_format:
        start: "{"
        current: "-"
        current_tip: ">"
        remain: "_"
        end: "}"
    attr:
        bg: red
        fg: "#f0f0f0"
    background_attr:
        bg: red
        fg: "#f0f0f0"
    frame_charset:
        horizontalbottom: '━'
        horizontaltop: '━'
        verticalleft: '┃'
        verticalright: '┃'
        topleft: '┏'
        topright: '┓'
        bottomleft: '┗'
        bottomright: '┛'
browser:
    workspace_bar: false
    tab_name_separator: 'XX'
    tabspaces: 4
    icons:
        default: x
        terminal: '&'
        console: '8'
        .go: $
        .py: 1 # ignored
    prompt:
        width: 20
        height: 10
        text_attr:
            fg: teal
        highlight_attr:
            bg: red
            fg: "#f0f0f0"
    window_manager:
        frame: true
        dim: false
        frame_attr:
            fg: red
        frame_charset:
            horizontalbottom: '━'
            horizontaltop: '━'
            verticalleft: '┃'
            verticalright: '┃'
            topleft: '┏'
            topright: '┓'
            bottomleft: '┗'
            bottomright: '┛'
        scroll_bar_attr:
            fg: "#f0f0f0"
        scroll_bar_char: '|'
        scroll_bar_hover_char: 'X'
        window_bar: true
        window_bar_charset:
            left: '▓'
            horizontal: '▒'
            right: '░'
        close_icon: 'x'
        close_icon_attr:
            fg: yellow
    frameunion_charset:
        left: '┣'
        right: '┫'
        top: '┫'
        bottom: '┫'
    message_bar_attr:
        fg: white
        bg: teal
    focus_tab_attr:
        fg: "#f0f0f0"
    non_focus_tab_attr:
        fg: white

workspace:
    auto_restore: false
    wallpaper: abc
    wallpaper_attr:
        fg: yellow
        bg: white
    wallpaper_background_attr:
        bg: white

terminal:
    plugin:
        bar_align_bottom: true
        bar_layout: ' {{ .StatusIcon | bg "gray" | fg "white" }} █▓▒░{{ .AlignCenter}}{{ .Command | fg "white" | bold }}{{ .AlignRight }}  ░▒▓█ {{ .Elapsed | fg "white" | bg "gray" }} '
        status_error_icon: "X"
        status_error_attr:
            fg: yellow
        status_success_icon: "$"
        status_success_attr:
            fg: blue
        animation: "ABC"
        bar_background_attr:
            bg: gray
    shell: sh
    max_lines: 999
    bell_trigger: "\x07"
    modal: true
    dynamic_tab_name: true
    initial_reservoir: 0
    attr:
        fg: white
        bg: yellow
    selection_attr:
        fg: green
        bg: teal
    needs_attention_attr:
        fg: red
        flags:
          - blink
`

func assertDefaultConfig(t *testing.T, cfg *ideConfig) {
	assert.Len(t, cfg.extensions(), 0)
	assert.Equal(t, 4, cfg.editorTabspaces())
	assert.NotNil(t, cfg.wallpaper())
	defWmConfig := handler.DefaultWindowManagerConfig()
	defWmConfig.FocusFrameAttr = defWmConfig.FrameAttr
	defWmConfig.FocusFrameCharSet = defWmConfig.FrameCharSet
	assert.Equal(t, defWmConfig, cfg.windowManagerConfig())
	assert.Equal(t, "", cfg.logOutputPath())
	level, slogLevel := cfg.logLevel()
	assert.Equal(t, logrus.ErrorLevel, level)
	assert.Equal(t, slog.LevelError, slogLevel)
	assert.Equal(t, term.InputCurrent, cfg.inputMode())
	assert.Equal(t, component.DefaultFrameUnionCharSet(), cfg.frameUnionCharset())
	assert.True(t, cfg.frameUnion())
	assert.Equal(t, text.DefaultConfig().Icons, cfg.icons())
	assert.Equal(t, idelsp.DefaultIconSet(), cfg.lspIcons())

	assert.Equal(t, workspaceBarKindNumbers, cfg.workspaceBarKind())

	auxBar := cfg.auxiliaryBarEnabled()
	assert.False(t, auxBar)

	folds := cfg.auxiliaryBarFolds()
	assert.False(t, folds)

	git := cfg.auxiliaryBarGit()
	assert.False(t, git)

	gitIcons := cfg.gitIconsEnabled()
	assert.False(t, gitIcons)

	iconsBar := cfg.iconsBarEnabled()
	assert.False(t, iconsBar)

	statusBar := cfg.statusBarEnabled()
	assert.True(t, statusBar)

	enabled, absolute := cfg.auxiliaryBarLines()
	assert.True(t, enabled)
	assert.True(t, absolute)

	cursor := cfg.auxiliaryBarHighlightCursor()
	assert.True(t, cursor)

	actualNotifications := cfg.notificationsConfig()
	expectedNotifications := defaultNotificationsConfig()
	setNotificationsColor(&expectedNotifications)
	assert.NotNil(t, actualNotifications.Interrupter)
	actualNotifications.Interrupter = nil
	expectedNotifications.Interrupter = nil
	assert.Equal(t, expectedNotifications, actualNotifications)

	assert.Equal(t, browser.DefaultConfig().FocusTabAttr, cfg.focusTabAttr())
	assert.Equal(t, browser.DefaultConfig().NonFocusTabAttr, cfg.nonFocusTabAttr())
	assert.Equal(t, term.Attributes{}, cfg.workspaceWallpaperAttr())
	assert.Equal(t, term.Attributes{}, cfg.workspaceWallpaperBackgroundAttr())
	selectAttr := term.Attributes{Attrs: term.AttrReverse}

	reservoir := cfg.initialTerminalCapacity()
	assert.Equal(t, 1, reservoir)

	barConfig := cfg.pluginBarConfig()
	expectedBarConfig := plugin.DefaultBarConfig()
	assert.Equal(t, expectedBarConfig, barConfig)

	vteConfig := cfg.terminalConfig()
	assert.NotNil(t, vteConfig.RingBell)
	assert.NotNil(t, vteConfig.ScheduleNextTick)
	vteConfig.ScheduleNextTick = nil
	vteConfig.RingBell = nil
	assertTerminalSearchConfig(t, &vteConfig, cfg.standardResultAttr(), selectAttr)
	assert.Equal(t, vte.Config{
		Clipboard:                cfg.clipboard(),
		SelectionAttributes:      selectAttr,
		NeedsAttentionAttributes: term.Attributes{Attrs: term.AttrBlink},
		Modal:                    true,
		ClipboardRegister:        clipboard.DefaultRegisterID,
		MaxLines:                 10_000,
		MinWidth:                 defaultMinWidth,
	}, vteConfig)
	assert.Equal(t, command.DefaultConfig().ShowManual, cfg.commandOverlayShowManual())
	assert.Equal(t, command.DefaultConfig().ManualAttr, cfg.commandOverlayManualAttr())
	assert.Zero(t, cfg.defaultAttr())

	assert.Equal(t, term.Attributes{Fg: term.ColorBlack, Bg: term.ColorYellow},
		cfg.standardResultAttr())
	assert.Equal(t, term.Attributes{}, cfg.standardAttr())
	assert.Equal(t, cfg.standardResultAttr(), cfg.emacsResultAttr())
	assert.Equal(t, term.Attributes{}, cfg.emacsMessageBarAttr())
	assert.Equal(t, term.Attributes{}, cfg.modalMessageBarAttr())
	assert.Equal(t, cfg.standardAttr(), cfg.emacsAttr())
	assert.Equal(t, term.Attributes{}, cfg.modalAttr())
	assert.True(t, cfg.autoRestore())
	assert.Equal(t, "  ", cfg.tabNameSeparator())
	assert.Equal(t, 90, cfg.editorRuler())
	assert.False(t, cfg.editorAutoPair())
	assert.False(t, cfg.editorAutoSave())

	expectedSyntaxConfig := syntax.DefaultConfig()
	syntaxConfig := cfg.syntaxConfig()
	assert.NotNil(t, syntaxConfig.ScheduleNextTick)
	syntaxConfig.ScheduleNextTick = nil
	expectedSyntaxConfig.ScheduleNextTick = nil
	assert.Equal(t, expectedSyntaxConfig, syntaxConfig)
	assert.Empty(t, cfg.editorComments())

	assert.Equal(t, "modal", cfg.editorMode())
	os.Setenv("SHELL", "fish")
}

func TestConfigDefault(t *testing.T) {
	ret := new(ideConfig)
	initDefaultConfig(ret, browser.NopWallpaper(),
		term.RingBell, term.ScheduleNextTick, "", "")
	assertDefaultConfig(t, ret)
}

// assertTerminalSearchConfig pins the terminal's scrollback search and
// clears it, so callers can compare the rest of the config as a literal.
func assertTerminalSearchConfig(t *testing.T, cfg *vte.Config, match, current term.Attributes) {
	t.Helper()
	search := cfg.Search
	cfg.Search = vte.SearchConfig{}
	assert.NotNil(t, search.Editor, "search is enabled")
	assert.Nil(t, search.WindowManager, "the terminal draws the box itself")
	assert.Equal(t, term.KeyComb{Mod: term.ModMeta, Ch: 'f'}, search.FindKey)
	assert.Zero(t, search.ReplaceKey, "terminals cannot replace")
	assert.Equal(t, match, search.MatchAttr)
	assert.Equal(t, current, search.CurrentMatchAttr)
}

func TestStandardSearchConfigDefaults(t *testing.T) {
	cfg := &ideConfig{errors: map[string]error{}}
	wm := currentWorkspaceWindowManager{}

	actual := cfg.standardSearchConfig(wm)

	assert.Equal(t, wm, actual.WindowManager)
	assert.Equal(t, term.KeyComb{Mod: term.ModMeta, Ch: 'f'}, actual.FindKey)
	assert.Equal(t, term.KeyComb{Mod: term.ModMeta, Ch: 'r'}, actual.ReplaceKey)
	assert.Equal(t, term.Attributes{}, actual.Attr)
	assert.Equal(t, actual.Attr, actual.InputAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorGray}, actual.PlaceholderAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorGray}, actual.FrameAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorSilver}, actual.FocusFrameAttr)
	assert.Equal(t, term.Attributes{Bg: term.ColorGray}, actual.ButtonAttr)
	assert.Equal(t, term.Attributes{Bg: term.ColorBlue}, actual.ButtonHoverAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorBlack, Bg: term.ColorYellow}, actual.MatchAttr)
	assert.Equal(t, actual.Attr, actual.CurrentMatchAttr)
	assert.Equal(t, actual.Attr, actual.StatusAttr)
	assert.Empty(t, cfg.errors)
}

func TestStandardConfigNamespaceResolution(t *testing.T) {
	tests := []struct {
		name       string
		editor     map[string]any
		want       term.Attributes
		wantErrors []string
	}{
		{
			name: "standard",
			editor: map[string]any{
				"standard": map[string]any{"attr": map[string]any{"fg": "green"}},
			},
			want: term.Attributes{Fg: term.ColorGreen},
		},
		{
			name: "modeless namespace ignored",
			editor: map[string]any{
				"modeless": map[string]any{"attr": map[string]any{"fg": "yellow"}},
			},
			want: term.Attributes{},
		},
		{
			name: "standard wins",
			editor: map[string]any{
				"standard": map[string]any{"attr": map[string]any{"fg": "green"}},
				"modeless": map[string]any{"attr": map[string]any{"fg": "yellow"}},
			},
			want: term.Attributes{Fg: term.ColorGreen},
		},
		{
			name: "malformed standard does not fall back",
			editor: map[string]any{
				"standard": "bad",
				"modeless": map[string]any{"attr": map[string]any{"fg": "yellow"}},
			},
			wantErrors: []string{"editor.standard"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{"editor": tt.editor}, errors: map[string]error{}}
			assert.Equal(t, tt.want, cfg.standardAttr())
			for _, path := range tt.wantErrors {
				assert.Error(t, cfg.errors[path])
			}
			assert.Len(t, cfg.errors, len(tt.wantErrors))
		})
	}
}

func TestStandardSearchConfigNestedOverrides(t *testing.T) {
	attrs := map[string]any{
		"attr":               map[string]any{"fg": "red"},
		"input_attr":         map[string]any{"fg": "green"},
		"placeholder_attr":   map[string]any{"fg": "yellow"},
		"frame_attr":         map[string]any{"fg": "blue"},
		"focus_frame_attr":   map[string]any{"fg": "purple"},
		"button_attr":        map[string]any{"fg": "teal"},
		"button_hover_attr":  map[string]any{"fg": "silver"},
		"match_attr":         map[string]any{"fg": "maroon"},
		"current_match_attr": map[string]any{"fg": "gray"},
		"status_attr":        map[string]any{"fg": "white"},
	}
	search := map[string]any{"find_key": "<c-g>", "replace_key": "<c-r>"}
	for key, value := range attrs {
		search[key] = value
	}
	cfg := &ideConfig{cfg: map[string]any{"editor": map[string]any{
		"standard": map[string]any{
			"attr":        map[string]any{"bg": "black"},
			"search_attr": map[string]any{"bg": "yellow"},
			"search":      search,
		},
	}}, errors: map[string]error{}}

	actual := cfg.standardSearchConfig(currentWorkspaceWindowManager{})

	assert.Equal(t, term.KeyComb{Mod: term.ModCtrl, Ch: 'g'}, actual.FindKey)
	assert.Equal(t, term.KeyComb{Mod: term.ModCtrl, Ch: 'r'}, actual.ReplaceKey)
	assert.Equal(t, term.Attributes{Fg: term.ColorRed}, actual.Attr)
	assert.Equal(t, term.Attributes{Fg: term.ColorGreen}, actual.InputAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorYellow}, actual.PlaceholderAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorBlue}, actual.FrameAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorPurple}, actual.FocusFrameAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorTeal}, actual.ButtonAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorSilver}, actual.ButtonHoverAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorMaroon}, actual.MatchAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorGray}, actual.CurrentMatchAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorWhite}, actual.StatusAttr)
	assert.Empty(t, cfg.errors)
}

func TestStandardSearchConfigIgnoresModelessNamespaceAndEmacsUnchanged(t *testing.T) {
	cfg := &ideConfig{cfg: map[string]any{"editor": map[string]any{
		"modeless": map[string]any{
			"attr":        map[string]any{"fg": "green"},
			"search_attr": map[string]any{"fg": "yellow"},
			"search":      map[string]any{"input_attr": map[string]any{"fg": "red"}},
		},
		"emacs": map[string]any{
			"attr":        map[string]any{"fg": "purple"},
			"message_bar": map[string]any{"attr": map[string]any{"fg": "teal"}},
			"search_attr": map[string]any{"fg": "maroon"},
		},
	}}, errors: map[string]error{}}

	actual := cfg.standardSearchConfig(currentWorkspaceWindowManager{})

	assert.Equal(t, term.Attributes{Fg: term.ColorBlack, Bg: term.ColorYellow}, actual.MatchAttr)
	assert.Equal(t, term.Attributes{}, actual.StatusAttr)
	assert.Equal(t, term.Attributes{}, actual.InputAttr)
	assert.Equal(t, term.Attributes{Fg: term.ColorMaroon}, cfg.emacsResultAttr())
	assert.Equal(t, term.Attributes{Fg: term.ColorTeal}, cfg.emacsMessageBarAttr())
	assert.Equal(t, term.Attributes{Fg: term.ColorPurple}, cfg.emacsAttr())
	assert.Empty(t, cfg.errors)
}

func TestStandardSearchConfigMalformedPaths(t *testing.T) {
	tests := []struct {
		name, path string
		search     any
	}{
		{name: "search", path: "editor.standard.search", search: "bad"},
		{name: "find key type", path: "editor.standard.search.find_key", search: map[string]any{"find_key": true}},
		{name: "find key syntax", path: "editor.standard.search.find_key", search: map[string]any{"find_key": "<not-a-key>"}},
		{name: "replace key", path: "editor.standard.search.replace_key", search: map[string]any{"replace_key": []any{}}},
		{name: "attribute", path: "editor.standard.search.button_attr", search: map[string]any{"button_attr": "bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{"editor": map[string]any{
				"standard": map[string]any{"search": tt.search},
			}}, errors: map[string]error{}}
			_ = cfg.standardSearchConfig(currentWorkspaceWindowManager{})
			assert.Error(t, cfg.errors[tt.path])
			assert.Len(t, cfg.errors, 1)
		})
	}
}

var _ standard.SearchWindowManager = currentWorkspaceWindowManager{}

func TestTerminalSearchConfigDefaultsToStandardFindKey(t *testing.T) {
	cfg := &ideConfig{cfg: map[string]any{"editor": map[string]any{
		"standard": map[string]any{"search": map[string]any{"find_key": "<c-g>"}},
	}}, errors: map[string]error{}}

	actual := cfg.terminalSearchConfig()

	assert.Equal(t, term.KeyComb{Mod: term.ModCtrl, Ch: 'g'}, actual.FindKey,
		"terminal.search.find_key is not set, so it follows the standard editor's")
	assert.Empty(t, cfg.errors)
}

func TestTerminalSearchConfigFindKeyOverride(t *testing.T) {
	cfg := &ideConfig{cfg: map[string]any{
		"editor":   map[string]any{"standard": map[string]any{"search": map[string]any{"find_key": "<c-g>"}}},
		"terminal": map[string]any{"search": map[string]any{"find_key": "<c-p>"}},
	}, errors: map[string]error{}}

	actual := cfg.terminalSearchConfig()

	assert.Equal(t, term.KeyComb{Mod: term.ModCtrl, Ch: 'p'}, actual.FindKey,
		"terminal.search.find_key overrides the standard editor's find_key")
	assert.Empty(t, cfg.errors)
}

func TestTerminalSearchConfigMalformedFindKey(t *testing.T) {
	tests := []struct {
		name, path string
		search     any
	}{
		{name: "search", path: "terminal.search", search: "bad"},
		{name: "find key type", path: "terminal.search.find_key", search: map[string]any{"find_key": true}},
		{name: "find key syntax", path: "terminal.search.find_key", search: map[string]any{"find_key": "<not-a-key>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"terminal": map[string]any{"search": tt.search},
			}, errors: map[string]error{}}
			_ = cfg.terminalSearchConfig()
			assert.Error(t, cfg.errors[tt.path])
			assert.Len(t, cfg.errors, 1)
		})
	}
}

func TestUpdatesAutoInstall(t *testing.T) {
	for _, tc := range []struct {
		name    string
		updates any
		want    bool
		wantErr bool
	}{
		{"absent updates defaults false", nil, false, false},
		{"absent key defaults false", map[string]any{}, false, false},
		{"explicit true", map[string]any{"auto_install": true}, true, false},
		{"explicit false", map[string]any{"auto_install": false}, false, false},
		{"invalid type defaults false", map[string]any{"auto_install": "yes"},
			false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.updates != nil {
				m["updates"] = tc.updates
			}
			cfg := &ideConfig{cfg: m, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.updatesAutoInstall())
			if tc.wantErr {
				assert.Error(t, cfg.errors["updates.auto_install"])
			} else {
				assert.Empty(t, cfg.errors)
			}
		})
	}
}

func TestTelemetryEnabled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		telemetry any
		want      bool
		wantErr   bool
	}{
		{"absent telemetry defaults true", nil, true, false},
		{"absent key defaults true", map[string]any{}, true, false},
		{"explicit true", map[string]any{"enabled": true}, true, false},
		{"explicit false", map[string]any{"enabled": false}, false, false},
		{"invalid type fails closed", map[string]any{"enabled": "yes"},
			false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.telemetry != nil {
				m["telemetry"] = tc.telemetry
			}
			cfg := &ideConfig{cfg: m, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.telemetryEnabled())
			assert.Equal(t, tc.want, TelemetryEnabled(config.MapConfig(m)))
			if tc.wantErr {
				assert.Error(t, cfg.errors["telemetry.enabled"])
			} else {
				assert.Empty(t, cfg.errors)
			}
		})
	}
}

func TestAuthorizerAutoAuthorize(t *testing.T) {
	for _, tc := range []struct {
		name           string
		authorizer     any
		wantExtensions bool
		wantCommands   bool
		wantErrKeys    []string
	}{
		{"absent authorizer uses defaults", nil, true, false, nil},
		{"absent keys use defaults", map[string]any{}, true, false, nil},
		{"neither", map[string]any{
			"auto_authorize_extensions": false,
			"auto_authorize_commands":   false,
		}, false, false, nil},
		{"extensions only", map[string]any{
			"auto_authorize_extensions": true,
			"auto_authorize_commands":   false,
		}, true, false, nil},
		{"commands only", map[string]any{
			"auto_authorize_extensions": false,
			"auto_authorize_commands":   true,
		}, false, true, nil},
		{"both", map[string]any{
			"auto_authorize_extensions": true,
			"auto_authorize_commands":   true,
		}, true, true, nil},
		{"invalid extensions fails closed", map[string]any{
			"auto_authorize_extensions": "yes",
		}, false, false, []string{"authorizer.auto_authorize_extensions"}},
		{"invalid commands fails closed", map[string]any{
			"auto_authorize_commands": "yes",
		}, true, false, []string{"authorizer.auto_authorize_commands"}},
		{"legacy key is ignored", map[string]any{
			"auto_authorize": true,
		}, true, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.authorizer != nil {
				m["authorizer"] = tc.authorizer
			}
			cfg := &ideConfig{cfg: m, errors: map[string]error{}}
			assert.Equal(t, tc.wantExtensions, cfg.authorizerAutoAuthorizeExtensions())
			assert.Equal(t, tc.wantCommands, cfg.authorizerAutoAuthorizeCommands())
			assert.Len(t, cfg.errors, len(tc.wantErrKeys))
			for _, key := range tc.wantErrKeys {
				assert.Error(t, cfg.errors[key])
			}
		})
	}
}

// TestTerminalModalDefaultFromEditorMode asserts that when terminal.modal
// is not set its default follows editor.mode: modal editors default to
// modal terminals, modeless to modeless, and exo follows its fallback.
func TestTerminalModalDefaultFromEditorMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		editor map[string]any
		want   bool
	}{
		{"unset editor defaults modal", nil, true},
		{"modal", map[string]any{"mode": "modal"}, true},
		{"modeless", map[string]any{"mode": "modeless"}, false},
		{"exo fallback modal", map[string]any{
			"mode": "exo",
			"exo":  map[string]any{"command": "vim {file}", "fallback": "modal"},
		}, true},
		{"exo fallback modeless", map[string]any{
			"mode": "exo",
			"exo":  map[string]any{"command": "vim {file}", "fallback": "modeless"},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.editor != nil {
				m["editor"] = tc.editor
			}
			cfg := &ideConfig{cfg: m, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.terminalModal())
		})
	}

	// An explicit terminal.modal always wins over the editor-mode default.
	cfg := &ideConfig{cfg: map[string]any{
		"editor":   map[string]any{"mode": "modeless"},
		"terminal": map[string]any{"modal": true},
	}, errors: map[string]error{}}
	assert.True(t, cfg.terminalModal())

	cfg = &ideConfig{cfg: map[string]any{
		"editor":   map[string]any{"mode": "modal"},
		"terminal": map[string]any{"modal": false},
	}, errors: map[string]error{}}
	assert.False(t, cfg.terminalModal())
}

// TestEditorModeNormalizesModelessToStandard pins that editorMode()
// resolves the deprecated "modeless" alias and the new "standard"
// value to editorModeStandard, while modal/exo/emacs pass through and
// an unknown mode falls back to modal.
func TestEditorModeNormalizesModelessToStandard(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"modeless", editorModeStandard},
		{"standard", editorModeStandard},
		{"emacs", editorModeEmacs},
		{"modal", editorModeModal},
		{"helix", editorModeHelix},
		{"exo", editorModeExo},
		{"bogus", editorModeModal},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"editor": map[string]any{"mode": tc.mode},
			}, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.editorMode())
		})
	}
}

// TestHelixIsAModalEditorMode pins that helix travels the same gating
// paths as vi: it is a legal editor.exo.fallback, and the console input
// line and the terminal keymap default to modal for it.
func TestHelixIsAModalEditorMode(t *testing.T) {
	t.Parallel()

	t.Run("exo fallback accepts helix", func(t *testing.T) {
		t.Parallel()
		canonical, ok := normalizeEditorFallback("helix")
		require.True(t, ok)
		assert.Equal(t, editorFallbackHelix, canonical)

		c := ideConfig{cfg: map[string]any{
			"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "helix"},
			},
		}, errors: map[string]error{}}
		assert.Equal(t, editorFallbackHelix, c.exoFallback())
		assert.Equal(t, editorModeHelix, c.pkgEditorMode())
	})

	for _, tc := range []struct {
		mode string
		want bool
	}{
		{editorModeModal, true},
		{editorModeHelix, true},
		{editorModeStandard, false},
		{editorModeEmacs, false},
	} {
		t.Run(tc.mode+" modal gating", func(t *testing.T) {
			c := ideConfig{cfg: map[string]any{
				"editor": map[string]any{"mode": tc.mode},
			}, errors: map[string]error{}}
			assert.Equal(t, tc.want, modalEditorMode(c.pkgEditorMode()))
			assert.Equal(t, tc.want, c.consoleEditorModal())
			assert.Equal(t, tc.want, c.terminalModalDefault())
		})
	}

	t.Run("helix keeps the modal command key", func(t *testing.T) {
		t.Parallel()
		c := ideConfig{cfg: map[string]any{
			"editor": map[string]any{"mode": editorModeHelix},
		}, errors: map[string]error{}}
		assert.Equal(t, defaultModalCommandKey, c.commandKey())
	})
}

// TestDebuggerConfigsTemplates asserts that debuggerConfigs reads the
// optional launch/attach argument templates under debugger.<lang>,
// leaves them nil when absent (so the adapter falls back to its
// built-in defaults), and records a parse error for a non-string
// template value.
func TestDebuggerConfigsTemplates(t *testing.T) {
	t.Parallel()

	t.Run("templates parsed", func(t *testing.T) {
		t.Parallel()
		c := ideConfig{cfg: map[string]any{
			"debugger": map[string]any{
				"python": map[string]any{
					"command":    "python -m debugpy.adapter --host {host} --port {port}",
					"adapter_id": "debugpy",
					"launch": map[string]any{
						"request": "launch",
						"type":    "python",
					},
					"attach": map[string]any{
						"request": "attach",
					},
				},
			},
		}, errors: map[string]error{}}

		got := c.debuggerConfigs()
		require.Contains(t, got, "python")
		assert.Equal(t, idedebug.AdapterConfig{
			Command: []string{
				"python", "-m", "debugpy.adapter",
				"--host", "{host}", "--port", "{port}",
			},
			AdapterID: "debugpy",
			LaunchArgs: map[string]string{
				"request": "launch",
				"type":    "python",
			},
			AttachArgs: map[string]string{"request": "attach"},
		}, got["python"])
		assert.Empty(t, c.errors)
	})

	t.Run("absent templates are nil", func(t *testing.T) {
		t.Parallel()
		c := ideConfig{cfg: map[string]any{
			"debugger": map[string]any{
				"go": map[string]any{
					"command":    "dlv dap --listen={addr}",
					"adapter_id": "dlv-dap",
				},
			},
		}, errors: map[string]error{}}

		got := c.debuggerConfigs()
		require.Contains(t, got, "go")
		assert.Nil(t, got["go"].LaunchArgs)
		assert.Nil(t, got["go"].AttachArgs)
		assert.Empty(t, c.errors)
	})

	t.Run("non-string template value records error", func(t *testing.T) {
		t.Parallel()
		c := ideConfig{cfg: map[string]any{
			"debugger": map[string]any{
				"python": map[string]any{
					"command": "debugpy",
					"launch": map[string]any{
						"stopOnEntry": true,
					},
				},
			},
		}, errors: map[string]error{}}

		_ = c.debuggerConfigs()
		assert.Contains(t, c.errors, "debugger.python.launch.stopOnEntry")
	})
}

// TestDebuggerConfigsConnectCommand asserts that a connect:// adapter
// command is validated at config load: the remainder must be a
// host:port endpoint and the command must carry no extra arguments,
// so a typo surfaces as a config error instead of a dial failure at
// session creation.
func TestDebuggerConfigsConnectCommand(t *testing.T) {
	t.Parallel()

	newConfig := func(command string) ideConfig {
		return ideConfig{cfg: map[string]any{
			"debugger": map[string]any{
				"python": map[string]any{"command": command},
			},
		}, errors: map[string]error{}}
	}

	t.Run("valid endpoint parses", func(t *testing.T) {
		t.Parallel()
		c := newConfig("connect://127.0.0.1:5678")
		got := c.debuggerConfigs()
		require.Contains(t, got, "python")
		assert.Equal(t, []string{"connect://127.0.0.1:5678"},
			got["python"].Command)
		assert.Empty(t, c.errors)
	})

	t.Run("missing port records error", func(t *testing.T) {
		t.Parallel()
		c := newConfig("connect://nohostport")
		got := c.debuggerConfigs()
		assert.NotContains(t, got, "python")
		assert.Contains(t, c.errors, "debugger.python.command")
	})

	t.Run("extra arguments record error", func(t *testing.T) {
		t.Parallel()
		c := newConfig("connect://127.0.0.1:5678 extra")
		got := c.debuggerConfigs()
		assert.NotContains(t, got, "python")
		assert.Contains(t, c.errors, "debugger.python.command")
	})
}

// TestHighlightTabCharEmptyDisables asserts that an explicitly empty
// focus_tab_highlight_char value disables the highlight (returns 0)
// while an absent key falls back to the browser default.
func TestHighlightTabCharEmptyDisables(t *testing.T) {
	def := browser.DefaultConfig().FocusTabHighlightChar

	// key absent → default
	cfg := &ideConfig{cfg: map[string]any{
		"browser":   map[string]any{},
		"workspace": map[string]any{},
	}, errors: map[string]error{}}
	assert.Equal(t, def, cfg.highlightTabChar(),
		"absent browser.focus_tab_highlight_char must fall back to default")
	assert.Equal(t, def, cfg.workspaceHighlightTabChar(),
		"absent workspace.focus_tab_highlight_char must fall back to default")

	// key present and empty → disabled (rune 0)
	cfg = &ideConfig{cfg: map[string]any{
		"browser":   map[string]any{"focus_tab_highlight_char": ""},
		"workspace": map[string]any{"focus_tab_highlight_char": ""},
	}, errors: map[string]error{}}
	assert.Equal(t, rune(0), cfg.highlightTabChar(),
		"empty browser.focus_tab_highlight_char must disable the highlight")
	assert.Equal(t, rune(0), cfg.workspaceHighlightTabChar(),
		"empty workspace.focus_tab_highlight_char must disable the highlight")

	// key present and non-empty → first rune
	cfg = &ideConfig{cfg: map[string]any{
		"browser":   map[string]any{"focus_tab_highlight_char": "▔"},
		"workspace": map[string]any{"focus_tab_highlight_char": "▁"},
	}, errors: map[string]error{}}
	assert.Equal(t, '▔', cfg.highlightTabChar())
	assert.Equal(t, '▁', cfg.workspaceHighlightTabChar())
}

// TestTabOverrideIcon asserts browser.tab_override_icon parses
// correctly: absent → 0 (no override), empty → 0, non-empty → first
// rune.
func TestTabOverrideIcon(t *testing.T) {
	// key absent → 0
	cfg := &ideConfig{cfg: map[string]any{
		"browser": map[string]any{},
	}, errors: map[string]error{}}
	assert.Equal(t, rune(0), cfg.tabOverrideIcon(),
		"absent browser.tab_override_icon must return rune 0")

	// key empty → 0
	cfg = &ideConfig{cfg: map[string]any{
		"browser": map[string]any{"tab_override_icon": ""},
	}, errors: map[string]error{}}
	assert.Equal(t, rune(0), cfg.tabOverrideIcon(),
		"empty browser.tab_override_icon must return rune 0")

	// key present and non-empty → first rune
	cfg = &ideConfig{cfg: map[string]any{
		"browser": map[string]any{"tab_override_icon": "●"},
	}, errors: map[string]error{}}
	assert.Equal(t, '●', cfg.tabOverrideIcon())
}

// TestWorkspaceHome asserts workspace.home parses correctly: absent →
// "~", empty → "~", non-empty → the configured path.
func TestWorkspaceHome(t *testing.T) {
	// key absent → default "~"
	cfg := &ideConfig{cfg: map[string]any{
		"workspace": map[string]any{},
	}, errors: map[string]error{}}
	assert.Equal(t, "~", cfg.workspaceHome(),
		"absent workspace.home must default to ~")
	assert.Empty(t, cfg.errors)

	// key empty → default "~"
	cfg = &ideConfig{cfg: map[string]any{
		"workspace": map[string]any{"home": ""},
	}, errors: map[string]error{}}
	assert.Equal(t, "~", cfg.workspaceHome(),
		"empty workspace.home must default to ~")
	assert.Empty(t, cfg.errors)

	// key present and non-empty → configured path
	cfg = &ideConfig{cfg: map[string]any{
		"workspace": map[string]any{"home": "~/work"},
	}, errors: map[string]error{}}
	assert.Equal(t, "~/work", cfg.workspaceHome())
}

func TestConfigDecodeError(t *testing.T) {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	_, err = f.WriteString("||\\n\x00{'BABY':'$$'}")
	require.NoError(t, err)

	var ret ideConfig
	// for assertDefaultConfig
	ret.cfg = map[string]any{
		"workspace": map[string]any{
			"wallpaper": "notEmpty",
		},
	}
	ret.ringBell = term.RingBell
	ret.scheduleNextTick = term.ScheduleNextTick
	err = loadFileConfig(&ret, f.Name())
	assert.Error(t, err)
	assertDefaultConfig(t, &ret)
}

func TestConfigSetting(t *testing.T) {
	m, err := decodeConfig(strings.NewReader(sampleConfig))
	require.NoError(t, err)

	var cfg ideConfig
	initConfig(&cfg, m, browser.NopWallpaper(),
		term.RingBell, term.ScheduleNextTick, "", "")
	cfg.storage = storagestub.NewInMemoryService()

	assert.Equal(t, 4, cfg.editorTabspaces())
	assert.Equal(t, 72, cfg.editorRuler())
	assert.True(t, cfg.editorAutoPair())
	_, ok := cfg.wallpaper().NewComponent().(component.String)
	assert.True(t, ok)
	assert.Equal(t, "/tmp/debug.log", cfg.logOutputPath())
	level, slogLevel := cfg.logLevel()
	assert.Equal(t, logrus.TraceLevel, level)
	assert.Equal(t, slog.LevelDebug, slogLevel)
	assert.True(t, term.InputMouse&cfg.inputMode() != 0)
	assert.True(t, term.InputEsc&cfg.inputMode() != 0)
	assert.Equal(t, "XX", cfg.tabNameSeparator())

	expectedConfig := handler.WindowManagerConfig{
		WindowManagerConfig: tcomponent.WindowManagerConfig{
			Frame:         true,
			FrameAttr:     term.Attributes{Fg: term.ColorRed},
			FrameCharSet:  component.FrameCharSetHighlight(),
			ScrollBarAttr: term.Attributes{Fg: term.GetColor("#f0f0f0")},
			ScrollBarChar: '|',
			NoMaxSize:     true,
			WindowBar:     true,
			WindowBarCharSet: tcomponent.WindowBarCharSet{
				Left:       '▓',
				Horizontal: '▒',
				Right:      '░',
			},
			CloseIcon:     'x',
			CloseIconAttr: term.Attributes{Fg: term.ColorYellow},
		},
		Dim:                false,
		ScrollBarHoverChar: 'X',
		FocusFrameAttr:     handler.DefaultWindowManagerConfig().FrameAttr,
		FocusFrameCharSet:  handler.DefaultWindowManagerConfig().FrameCharSet,
	}
	assert.Equal(t, expectedConfig, cfg.windowManagerConfig())
	assert.True(t, cfg.frameUnion())

	expectedIcons := text.IconSet{
		Directory:     '',
		OpenDirectory: '',
		Default:       'x',
		Terminal:      '&',
		Shell:         '8',
		Extensions:    map[string]rune{".go": '$'},
	}
	actualIcons := cfg.icons()
	assert.Equal(t, expectedIcons, actualIcons)
	assert.Equal(t, text.CommentConfig{
		"go": {
			Line:  []string{"//"},
			Block: []text.CommentBlock{{Start: "/*", End: "*/"}},
		},
	}, cfg.editorComments())
	assert.Equal(t, handler.LessMessageLayout{
		Template: " %s █▓▒░",
		Attributes: term.Attributes{
			Bg:    term.ColorRed,
			Fg:    term.ColorWhite,
			Attrs: term.AttrBold,
		},
	}, cfg.emacsMessageBarLayout())
	assert.Equal(t, handler.LessMessageLayout{
		Template: "▓▒░ %s ",
		Attributes: term.Attributes{
			Bg:    term.ColorNavy,
			Fg:    term.ColorSilver,
			Attrs: term.AttrItalic,
		},
	}, cfg.modalMessageBarLayout())
	expectedLSPIcons := idelsp.IconSet{
		idelsp.IconDiagnosticError:       "E",
		idelsp.IconDiagnosticWarning:     "W",
		idelsp.IconDiagnosticInformation: "I",
		idelsp.IconDiagnosticHint:        "H",
		idelsp.IconCompilerInline:        ">",
		idelsp.IconCompilerEscape:        "^",
		idelsp.IconCompilerBounds:        "B",
		idelsp.IconCompilerNilcheck:      "0",
		idelsp.IconCompilerDefault:       "C",
	}
	assert.Equal(t, expectedLSPIcons, cfg.lspIcons())

	assert.Equal(t, workspaceBarKindDisabled, cfg.workspaceBarKind())

	expectedCommandAliases := map[string]text.CommandAlias{
		"todo": text.CommandAlias{Name: "todo",
			Commands: []string{"e file:///tmp/todo.md", "jenesaisquoi"}},
		"cherry": text.CommandAlias{Name: "cherry", Commands: []string{"bomb"}},
		"parcels": text.CommandAlias{
			Name: "parcels",
			Commands: []string{
				"Somethinggreater",
				"NowIcaresomemore",
				"Comingback",
			},
		},
		"daynight": text.CommandAlias{
			Name:     "daynight",
			Commands: []string{"ram"},
		},
		"dtmf": text.CommandAlias{
			Name:     "dtmf",
			Commands: []string{"ram"},
		},
		"editmix": text.CommandAlias{
			Name:     "editmix",
			Commands: []string{"e"},
		},
		"static_with_hist": text.CommandAlias{
			Name:     "static_with_hist",
			Commands: []string{"m"},
		},
		"bad": text.CommandAlias{
			Name:     "bad",
			Commands: []string{"x"},
		},
	}
	actualCommandAliases := cfg.commandAliases()
	parcelsAlias := actualCommandAliases["parcels"]
	require.Len(t, parcelsAlias.Completers, 1)
	assert.NotNil(t, parcelsAlias.Completers[0])
	parcelsAlias.Completers = nil
	actualCommandAliases["parcels"] = parcelsAlias

	daynight := actualCommandAliases["daynight"]
	require.Len(t, daynight.Completers, 1)
	require.NotNil(t, daynight.Completers[0])
	it, _, err := daynight.Completers[0](new(text.Component)).
		Complete(context.Background(), []string{})
	require.NoError(t, err)
	daynight.Completers = nil
	actualCommandAliases["daynight"] = daynight

	dtmf := actualCommandAliases["dtmf"]
	require.Len(t, dtmf.Completers, 1)
	assert.NotNil(t, dtmf.Completers[0])
	dtmf.Completers = nil
	actualCommandAliases["dtmf"] = dtmf

	editmix := actualCommandAliases["editmix"]
	require.Len(t, editmix.Completers, 2)
	assert.NotNil(t, editmix.Completers[0])
	assert.NotNil(t, editmix.Completers[1])
	editmix.Completers = nil
	actualCommandAliases["editmix"] = editmix

	staticWithHist := actualCommandAliases["static_with_hist"]
	require.Len(t, staticWithHist.Completers, 2)
	assert.NotNil(t, staticWithHist.Completers[0])
	// second factory exposes the static options "a" and "B"
	staticIt, _, sterr := staticWithHist.Completers[1](new(text.Component)).
		Complete(context.Background(), []string{})
	require.NoError(t, sterr)
	staticOpts, sterr := iterator.ToSlice(context.Background(), staticIt)
	require.NoError(t, sterr)
	assert.Equal(t, []string{"a", "B"}, staticOpts)
	staticWithHist.Completers = nil
	actualCommandAliases["static_with_hist"] = staticWithHist

	bad := actualCommandAliases["bad"]
	// {nope} is unknown so no factories are produced.
	assert.Empty(t, bad.Completers)
	bad.Completers = nil
	actualCommandAliases["bad"] = bad
	// the parser should record an error under
	// command.aliases.bad.completer (or command.aliases when aggregated).
	_, hasErr := cfg.errors["command.aliases"]
	assert.True(t, hasErr, "expected parser error for {nope} placeholder")

	actualOptions, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "B"}, actualOptions)

	assert.Equal(t, expectedCommandAliases, actualCommandAliases)
	assert.False(t, cfg.commandOverlayShowManual())
	assert.Equal(t, term.Attributes{Fg: term.ColorBlack, Bg: term.ColorYellow, Attrs: term.AttrBold},
		cfg.commandOverlayManualAttr())

	expectedFUCs := component.DefaultFrameUnionCharSet()
	expectedFUCs.HorizontalBottom = '━'
	expectedFUCs.HorizontalTop = '━'
	expectedFUCs.VerticalLeft = '┃'
	expectedFUCs.VerticalRight = '┃'
	expectedFUCs.TopLeft = '┏'
	expectedFUCs.TopRight = '┓'
	expectedFUCs.BottomLeft = '┗'
	expectedFUCs.BottomRight = '┛'
	expectedFUCs.Left = '┣'
	expectedFUCs.Right = '┫'
	expectedFUCs.Top = '┫'
	expectedFUCs.Bottom = '┫'
	assert.Equal(t, expectedFUCs, cfg.frameUnionCharset())

	statusBar := cfg.statusBarEnabled()
	assert.True(t, statusBar)

	statusBarCfg := cfg.statusBarConfig(workspaceapi.URI{}, nil, nil)
	assert.NotNil(t, statusBarCfg.ScheduleNextTick)
	statusBarCfg.ScheduleNextTick = nil
	assert.Equal(t, text.StatusBarConfig{
		BackgroundColor: term.ColorMaroon,
		Layout: []text.StatusBarComponent{
			{
				Template: "█%s█▓▒░",
				Type:     text.StatusBarStatus,
				Attributes: term.Attributes{
					Bg:    term.ColorRed,
					Fg:    term.ColorBlack,
					Attrs: term.AttrBold,
				},
			},
			{Template: "  %s", Type: text.StatusBarFilePath},
			{Template: "   %s", Type: text.StatusBarGitShortRef},
			{
				Template:   "   %d",
				Type:       text.StatusBarGitDiffAdded,
				Attributes: term.Attributes{Fg: term.ColorGreen},
			},
			{
				Template:   "   %d ",
				Type:       text.StatusBarGitDiffDeleted,
				Attributes: term.Attributes{Fg: term.ColorRed},
			},
			{Type: text.StatusBarVoid},
			{Template: "%d:", Type: text.StatusBarCoordinatesCursorX},
			{Template: "%d  ", Type: text.StatusBarCoordinatesCursorY},
			{Template: "%d lines  ", Type: text.StatusBarTotalLines},
			{
				Template:   "%s  ",
				Type:       text.StatusBarLanguage,
				Attributes: term.Attributes{Attrs: term.AttrBold},
			},
		},
		GitService: nil,
	}, statusBarCfg)

	auxBar := cfg.auxiliaryBarEnabled()
	assert.True(t, auxBar)

	folds := cfg.auxiliaryBarFolds()
	assert.True(t, folds)

	git := cfg.auxiliaryBarGit()
	assert.True(t, git)

	gitIcons := cfg.gitIconsEnabled()
	assert.True(t, gitIcons)

	iconsBar := cfg.iconsBarEnabled()
	assert.True(t, iconsBar)

	cursor := cfg.auxiliaryBarHighlightCursor()
	assert.False(t, cursor)

	enabled, absolute := cfg.auxiliaryBarLines()
	assert.True(t, enabled)
	assert.False(t, absolute)

	noti := cfg.notificationsConfig()
	assert.Equal(t, 1, noti.Padding)
	assert.Equal(t, notifications.ProgressRunes{
		Start:      '{',
		Current:    '-',
		CurrentTip: '>',
		Remain:     '_',
		End:        '}',
	}, noti.ProgressRunes)
	assert.False(t, noti.ProgressBar)
	assert.Equal(t, 1*time.Second, noti.AutoClose)
	assert.Equal(t, component.FrameCharSetHighlight(), noti.FrameCharSet)
	assert.Equal(t, term.Attributes{Fg: term.GetColor("#f0f0f0"), Bg: term.ColorRed},
		noti.Attributes)
	assert.Equal(t, term.Attributes{Fg: term.GetColor("#f0f0f0"), Bg: term.ColorRed},
		noti.BackgroundAttributes)

	assert.Equal(t, term.Attributes{Fg: term.GetColor("#f0f0f0")}, cfg.focusTabAttr())
	assert.Equal(t, term.Attributes{Fg: term.ColorWhite}, cfg.nonFocusTabAttr())
	assert.Equal(t, term.Attributes{Fg: term.ColorYellow, Bg: term.ColorWhite}, cfg.workspaceWallpaperAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorWhite}, cfg.workspaceWallpaperBackgroundAttr())

	reservoir := cfg.initialTerminalCapacity()
	assert.Equal(t, 0, reservoir)

	barConfig := cfg.pluginBarConfig()
	expectedBarConfig := plugin.BarConfig{
		StatusErrorIcon:       "X",
		StatusSuccessIcon:     "$",
		StatusErrorColor:      term.ColorYellow,
		StatusSuccessColor:    term.ColorBlue,
		StatusAnimationFrames: []string{"A", "B", "C"},
		BackgroundColor:       term.ColorGray,
		AlignBottom:           true,
		Layout: []plugin.BarComponent{
			{
				Type:     plugin.BarStatusIcon,
				Template: " %s █▓▒░",
				Attributes: term.Attributes{
					Fg: term.ColorWhite,
					Bg: term.ColorGray,
				},
			},
			{
				Type: plugin.BarAlignCenter,
			},
			{
				Type:     plugin.BarCommand,
				Template: "%s",
				Attributes: term.Attributes{
					Fg:    term.ColorWhite,
					Attrs: term.AttrBold,
				},
			},
			{
				Type:     plugin.BarAlignRight,
				Template: "  ",
			},
			{
				Type:     plugin.BarElapsed,
				Template: "░▒▓█ %s ",
				Attributes: term.Attributes{
					Fg: term.ColorWhite,
					Bg: term.ColorGray,
				},
			},
		},
	}
	assert.Equal(t, expectedBarConfig, barConfig)

	vteConfig := cfg.terminalConfig()
	assert.NotNil(t, vteConfig.ScheduleNextTick)
	vteConfig.ScheduleNextTick = nil
	assert.NotNil(t, vteConfig.RingBell)
	vteConfig.RingBell = nil
	assertTerminalSearchConfig(t, &vteConfig, cfg.standardResultAttr(),
		term.Attributes{Fg: term.ColorGreen, Bg: term.ColorTeal})
	expectedEmulatorConfig := vte.Config{
		CommandAndArgs:           []string{"sh"},
		Attributes:               term.Attributes{Fg: term.ColorWhite, Bg: term.ColorYellow},
		Clipboard:                cfg.clipboard(),
		ClipboardRegister:        clipboard.DefaultRegisterID,
		SelectionAttributes:      term.Attributes{Fg: term.ColorGreen, Bg: term.ColorTeal},
		NeedsAttentionAttributes: term.Attributes{Attrs: term.AttrBlink, Fg: term.ColorRed},
		Modal:                    true,
		DynamicTabName:           true,
		MaxLines:                 999,
		Bell:                     []byte{0x07},
		MinWidth:                 defaultMinWidth,
	}
	assert.Equal(t, expectedEmulatorConfig, vteConfig)

	expectedPrompt := browser.PromptConfig{
		TextAttr:      term.Attributes{Fg: term.ColorTeal},
		HighlightAttr: term.Attributes{Fg: term.GetColor("#f0f0f0"), Bg: term.ColorRed},
		MinWidth:      browser.DefaultConfig().MinWidth,
	}
	assert.Equal(t, expectedPrompt, cfg.promptConfig())

	assert.Equal(t, term.Attributes{Bg: term.ColorRed,
		Fg: term.GetColor("#f0f0f0")}, cfg.modalResultAttr())

	assert.Equal(t, term.Attributes{Bg: term.ColorRed,
		Fg: term.GetColor("#f1f1f1")}, cfg.standardResultAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorMaroon,
		Fg: term.GetColor("#f7f7f7")}, cfg.emacsResultAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorTeal,
		Fg: term.ColorWhite}, cfg.emacsMessageBarAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorNavy,
		Fg: term.ColorSilver}, cfg.modalMessageBarAttr())

	expectedSyntaxConfig := syntax.DefaultConfig()
	expectedSyntaxConfig.Autoindent = false
	expectedSyntaxConfig.CaptureNamesAttributes["function"] = term.Attributes{
		Fg: term.ColorGreen, Bg: term.ColorYellow}
	syntaxConfig := cfg.syntaxConfig()
	assert.NotNil(t, syntaxConfig.ScheduleNextTick)
	syntaxConfig.ScheduleNextTick = nil
	expectedSyntaxConfig.ScheduleNextTick = nil
	assert.Equal(t, expectedSyntaxConfig, syntaxConfig)

	assert.Equal(t, term.Attributes{Bg: term.ColorGreen,
		Fg: term.GetColor("#f9f9f9")}, cfg.standardAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorBlue,
		Fg: term.GetColor("#f8f8f8")}, cfg.emacsAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorYellow,
		Fg: term.GetColor("#f2f2f2")}, cfg.modalAttr())

	assert.Equal(t, "modal", cfg.editorMode())
	os.Setenv("SHELL", "")

	wantMappings := map[handler.Sequence][][]string{
		{First: term.KeyComb{Ch: 'f'}}:                    {{"searchfile"}},
		{First: term.KeyComb{Ch: 'l'}}:                    {{"searchtext"}},
		{First: term.KeyComb{Ch: 'x', Mod: term.ModCtrl}}: {{"closeDoors"}},
		{
			First: term.KeyComb{Ch: 'x', Mod: term.ModCtrl},
			Last:  term.KeyComb{Ch: 'p', Mod: term.ModCtrl},
		}: {{"openAllDoors"}},
		{
			First: term.KeyComb{Ch: 'f'},
			Last:  term.KeyComb{Ch: 'p', Mod: term.ModCtrl},
		}: {{"openDoors", "small"}},
		{
			First: term.KeyComb{Ch: 'x', Mod: term.ModCtrl},
			Last:  term.KeyComb{Ch: '9'},
		}: {{"openDoors", "1"}, {"large", "2"}},
	}
	assert.Equal(t, wantMappings, cfg.commandKeyMappings())
	assert.False(t, cfg.autoRestore())

	assert.Len(t, cfg.extensions(), 1)
	extensionCfgStruct := cfg.extensions()["fuzzy_search"]
	assert.Equal(t, "fuzzy_search", extensionCfgStruct.id)
	cfg.cfg["workspace"].(map[string]any)["wallpaper"] = ""
	extensionCfgStruct.parent.cfg["workspace"].(map[string]any)["wallpaper"] = ""
	cfg.defaultWallpaper.NewComponent = nil
	cfg.ringBell = nil
	cfg.scheduleNextTick = nil
	extensionCfgStruct.parent.defaultWallpaper.NewComponent = nil
	extensionCfgStruct.parent.ringBell = nil
	extensionCfgStruct.parent.scheduleNextTick = nil
	assert.Equal(t, &cfg, extensionCfgStruct.parent)

	extensionCfg, ok := extensionCfgStruct.config()
	require.True(t, ok)

	fileCfg, err := extensionCfg.GetConfig("file")
	require.NoError(t, err)
	cmd, err := fileCfg.GetString("command")
	require.NoError(t, err)
	assert.Equal(t, "ag -g \"\"", cmd)

	assert.NotZero(t, cfg.defaultAttr())
}

func TestConfigDim(t *testing.T) {
	cases := []struct {
		name      string
		dim       any
		wantDim   bool
		wantBW    bool
		wantError bool
	}{
		{name: "true", dim: true, wantDim: true, wantBW: false},
		{name: "false", dim: false, wantDim: false, wantBW: false},
		{name: "b&w", dim: "b&w", wantDim: true, wantBW: true},
		{name: "bw", dim: "bw", wantDim: true, wantBW: true},
		{name: "invalid string falls back to default", dim: "nope", wantDim: true, wantBW: false, wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"browser": map[string]any{
					"window_manager": map[string]any{
						"dim": tc.dim,
					},
				},
			}, errors: map[string]error{}}

			dim, bw := cfg.dim()
			assert.Equal(t, tc.wantDim, dim)
			assert.Equal(t, tc.wantBW, bw)

			wmCfg := cfg.windowManagerConfig()
			assert.Equal(t, tc.wantDim, wmCfg.Dim)
			assert.Equal(t, tc.wantBW, wmCfg.BW)

			if tc.wantError {
				assert.Contains(t, cfg.errors, "window_manager.dim")
			} else {
				assert.NotContains(t, cfg.errors, "window_manager.dim")
			}
		})
	}
}

func TestLoadEmbededConfig(t *testing.T) {
	var cfg ideConfig
	err := loadConfig(&cfg, "nonExistent", browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
}

func TestTutorialFilesDecoded(t *testing.T) {
	cfg := ideConfig{
		cfg: map[string]any{
			"tutorials": map[string]any{
				"basics":   "/etc/x.star",
				"advanced": "/etc/y.star",
			},
		},
		errors: map[string]error{},
	}
	got := cfg.tutorialFiles()
	require.Equal(t, 2, len(got))
	assert.Equal(t, "/etc/x.star", got["basics"])
	assert.Equal(t, "/etc/y.star", got["advanced"])
	assert.Empty(t, cfg.errors)
}

func TestTutorialFilesMissingReturnsNil(t *testing.T) {
	cfg := ideConfig{
		cfg:    map[string]any{},
		errors: map[string]error{},
	}
	assert.Nil(t, cfg.tutorialFiles())
	assert.Empty(t, cfg.errors)
}

func TestTutorialFilesWrongRootTypeRecordsError(t *testing.T) {
	cfg := ideConfig{
		cfg: map[string]any{
			"tutorials": "not a map",
		},
		errors: map[string]error{},
	}
	assert.Nil(t, cfg.tutorialFiles())
	require.NotNil(t, cfg.errors["tutorials"])
	assert.Contains(t, cfg.errors["tutorials"].Error(), "invalid type")
}

func TestTutorialFilesEntryWrongTypeRecordsError(t *testing.T) {
	cfg := ideConfig{
		cfg: map[string]any{
			"tutorials": map[string]any{
				"good": "/etc/ok.star",
				"bad":  42,
			},
		},
		errors: map[string]error{},
	}
	got := cfg.tutorialFiles()
	assert.Equal(t, map[string]string{"good": "/etc/ok.star"}, got)
	require.NotNil(t, cfg.errors["tutorials.bad"])
	assert.Contains(t, cfg.errors["tutorials.bad"].Error(),
		"expected string path")
}

func TestShellMaxHistoryFromConfig(t *testing.T) {
	f, err := os.CreateTemp("", "*.star")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`config = {
    "console": {
        "max_history": 7,
    },
}`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.consoleMaxHistory())
}

func TestShellModalStartInsertFromConfig(t *testing.T) {
	f, err := os.CreateTemp("", "*.star")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`config = {
    "console": {
        "modal_start_insert": False,
    },
}`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.False(t, cfg.consoleModalStartInsert())
}

func TestShellModalStartInsertDefaultsTrue(t *testing.T) {
	f, err := os.CreateTemp("", "*.star")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`config = {
    "console": {
        "max_history": 7,
    },
}`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.True(t, cfg.consoleModalStartInsert())
}

func TestConsolePromptFromConfig(t *testing.T) {
	f, err := os.CreateTemp("", "*.star")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`config = {
    "console": {
        "prompt": "rune> ",
    },
}`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.Equal(t, "rune> ", cfg.consolePrompt())
	assert.Equal(t, "rune> ", cfg.consoleCfg().prompt)
}

func TestConsolePromptDefaultsEmpty(t *testing.T) {
	f, err := os.CreateTemp("", "*.star")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`config = {
    "console": {
        "max_history": 7,
    },
}`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.Empty(t, cfg.consolePrompt())
}

// TestShellEditorModalFromEditorMode asserts consoleCfg().modal mirrors the
// editor backing the console prompt: modal is modal, modeless is not, and
// exo follows its configured fallback.
func TestShellEditorModalFromEditorMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		editor map[string]any
		want   bool
	}{
		{"unset editor defaults modal", nil, true},
		{"modal", map[string]any{"mode": "modal"}, true},
		{"modeless", map[string]any{"mode": "modeless"}, false},
		{"exo fallback modal", map[string]any{
			"mode": "exo",
			"exo":  map[string]any{"command": "vim {file}", "fallback": "modal"},
		}, true},
		{"exo fallback modeless", map[string]any{
			"mode": "exo",
			"exo":  map[string]any{"command": "vim {file}", "fallback": "modeless"},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.editor != nil {
				m["editor"] = tc.editor
			}
			cfg := &ideConfig{cfg: m, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.consoleCfg().modal)
		})
	}
}

func TestInvalidAliases(t *testing.T) {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`
command:
  aliases:
    meh:
      - yay
    yay:
      - nay
    nay: meh
`)
	require.NoError(t, err)

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alias cycle detected")
	aliases, _ := cfg.parseAliasCommands()
	assert.Empty(t, aliases)
}

func TestTabspaces(t *testing.T) {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`
editor:
  tabspaces: 2
`)
	require.NoError(t, err)

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.Equal(t, 2, cfg.editorTabspaces())
}

func TestEditorMaxSizeForSyntax(t *testing.T) {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`
editor:
  max_size_for_syntax: 2048
`)
	require.NoError(t, err)

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.Equal(t, 2048, cfg.editorMaxSizeForSyntax())
}

func TestEditorSwapDir(t *testing.T) {
	tsuite := []struct {
		name     string
		value    string
		want     bool
		wantErrs bool
	}{
		{name: "absent key keeps swaps in the data directory", value: "", want: true},
		{name: "enabled", value: "true", want: true},
		{name: "disabled keeps swaps next to the file", value: "false", want: false},
		{name: "malformed value", value: `"yes please"`, want: true, wantErrs: true},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			f, err := os.CreateTemp("", "")
			require.NoError(t, err)
			defer os.Remove(f.Name())

			src := "\neditor:\n  tabspaces: 4\n"
			if tcase.value != "" {
				src += "  swap_dir: " + tcase.value + "\n"
			}
			_, err = f.WriteString(src)
			require.NoError(t, err)

			var cfg ideConfig
			err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
				DefaultConfig{src: "config = {}"},
				term.RingBell, term.ScheduleNextTick, "")
			require.NoError(t, err)

			assert.Equal(t, tcase.want, cfg.editorSwapDir())
			if tcase.wantErrs {
				assert.Contains(t, cfg.errors, "editor.swap_dir")
			} else {
				assert.NotContains(t, cfg.errors, "editor.swap_dir")
			}
		})
	}
}

func TestEditorIndentType(t *testing.T) {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.WriteString(`
editor:
  indents:
    yaml: spaces
    go: tab
`)
	require.NoError(t, err)

	var cfg ideConfig
	err = loadConfig(&cfg, f.Name(), browser.NopWallpaper(),
		DefaultConfig{src: "config = {}"},
		term.RingBell, term.ScheduleNextTick, "")
	require.NoError(t, err)
	assert.Equal(t, text.IndentConfig{
		"yaml": text.IndentRuneSpace,
		"go":   text.IndentRuneTab,
	}, cfg.editorIndents())
}

// TestCommandKeyBindingLookup asserts that commandKeyBindingLookup
// inverts the configured key bindings: each command line resolves to
// its key and multi-command sequences map every line to the same key.
func TestCommandKeyBindingLookup(t *testing.T) {
	t.Parallel()
	c := ideConfig{
		cfg: map[string]any{
			"command": map[string]any{
				"key_bindings": map[string]any{
					"<m-n>":      "windownew",
					"<s-m-n>":    "windownew right",
					"<c-x><c-h>": "windowfocus left",
					"<m-d>":      []any{"openDoors 1", "large 2"},
				},
			},
		},
		errors: map[string]error{},
	}
	lookup := c.commandKeyBindingLookup()

	assert.Equal(t, "<meta-n>", lookup("windownew", nil))
	assert.Equal(t, "<shift-meta-n>", lookup("windownew", []string{"right"}))
	assert.Equal(t, "<ctrl-x><ctrl-h>",
		lookup("windowfocus", []string{"left"}))
	// Multi-command sequence: every command line maps to the key.
	assert.Equal(t, "<meta-d>", lookup("openDoors", []string{"1"}))
	assert.Equal(t, "<meta-d>", lookup("large", []string{"2"}))
	// A bare-command binding may perform a different action, so an
	// args-qualified miss must not advertise it.
	assert.Empty(t, lookup("windownew", []string{"down"}))
	// Unbound command yields "".
	assert.Equal(t, "", lookup("tabclose", nil))
	assert.Empty(t, c.errors)
}

func TestCommandKeyBindingLookupPrefersPrintableAlias(t *testing.T) {
	t.Parallel()
	c := ideConfig{
		cfg: map[string]any{
			"command": map[string]any{
				"key_bindings": map[string]any{
					"<c-a-m-left>": "windowresize decrease width",
					"<c-a-m-j>":    "windowresize decrease width",
				},
			},
		},
		errors: map[string]error{},
	}

	for range 20 {
		lookup := c.commandKeyBindingLookup()
		assert.Equal(t, "<ctrl-alt-meta-j>",
			lookup("windowresize", []string{"decrease", "width"}))
	}
	assert.Empty(t, c.errors)
}

// TestCommandKeyBindingLookupPrefersSingleChord pins that a command bound
// to both a single chord and a two-key sequence advertises the chord. A
// focused terminal consumes prefix chords such as C-x as PTY input, so
// surfacing the sequence would advertise a key the user cannot press.
func TestCommandKeyBindingLookupPrefersSingleChord(t *testing.T) {
	t.Parallel()
	c := ideConfig{
		cfg: map[string]any{
			"command": map[string]any{
				"key_bindings": map[string]any{
					"<c-x>2": "windownew down",
					"<m-d>":  "windownew down",
					"<c-x>0": "windowclose",
					"<f9>":   "lsp diagnostics",
					"<c-x>9": "lsp diagnostics",
				},
			},
		},
		errors: map[string]error{},
	}

	for range 20 {
		lookup := c.commandKeyBindingLookup()
		assert.Equal(t, "<meta-d>", lookup("windownew", []string{"down"}))
		// A named key still beats a two-key sequence.
		assert.Equal(t, "<f9>", lookup("lsp", []string{"diagnostics"}))
		// A sequence is still returned when it is the only binding.
		assert.Equal(t, "<ctrl-x>0", lookup("windowclose", nil))
	}
	assert.Empty(t, c.errors)
}

func TestCommandKeyBindings(t *testing.T) {
	t.Parallel()
	cfg := config.MapConfig(map[string]any{
		"command": map[string]any{
			"key_bindings": map[string]any{
				"<m-q>":        "quit",
				"<m-,>":        "config",
				"<c-x><c-c>":   "quit",
				"<c-x>0":       "windowclose",
				"<c-a-m-left>": "windowresize decrease width",
				"<c-a-m-j>":    "windowresize decrease width",
				"<m-d>":        []any{"openDoors 1", "large 2"},
			},
		},
	})

	for range 20 {
		lookup := CommandKeyBindings(cfg)
		assert.Equal(t, term.KeyComb{Mod: term.ModMeta, Ch: 'q'}, lookup["quit"])
		assert.Equal(t, term.KeyComb{Mod: term.ModMeta, Ch: ','}, lookup["config"])
		// Printable chords win over named-key aliases.
		assert.Equal(t, term.KeyComb{Mod: term.ModCtrlAltMeta, Ch: 'j'},
			lookup["windowresize decrease width"])
		// Every command line of a multi-command binding maps to the key.
		assert.Equal(t, term.KeyComb{Mod: term.ModMeta, Ch: 'd'}, lookup["openDoors 1"])
		assert.Equal(t, term.KeyComb{Mod: term.ModMeta, Ch: 'd'}, lookup["large 2"])
		// A two-key sequence cannot be a menu accelerator.
		assert.NotContains(t, lookup, "windowclose")
		assert.NotContains(t, lookup, "tabclose")
	}
}

func TestCommandKeyBindingsWithoutCommandConfig(t *testing.T) {
	t.Parallel()
	assert.Empty(t, CommandKeyBindings(config.MapConfig(map[string]any{})))
}

func quickMenuEntry(symbol, title string, command any) map[string]any {
	return map[string]any{"symbol": symbol, "title": title, "command": command}
}

func TestQuickMenuButtons(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		entries  any
		want     []QuickMenuButton
		wantErrs []string
	}{{
		name: "preserves order and accepts both command spellings",
		entries: []any{
			quickMenuEntry("plus", "Open Project", "workspaceopen"),
			quickMenuEntry("terminal", "New Terminal", []any{"windownew", "right"}),
		},
		want: []QuickMenuButton{
			{Symbol: "plus", Title: "Open Project", Command: []string{"workspaceopen"}},
			{Symbol: "terminal", Title: "New Terminal",
				Command: []string{"windownew", "right"}},
		},
	}, {
		name:     "section must be a list",
		entries:  map[string]any{"plus": "workspaceopen"},
		wantErrs: []string{"gui.quick_menu"},
	}, {
		name:     "entry must be a dict",
		entries:  []any{"workspaceopen"},
		wantErrs: []string{"gui.quick_menu[0]"},
	}, {
		name:     "symbol is required",
		entries:  []any{map[string]any{"command": "help"}},
		wantErrs: []string{"gui.quick_menu[0].symbol"},
	}, {
		name:     "symbol charset is restricted",
		entries:  []any{quickMenuEntry("plus\x00; rm -rf /", "Bad", "help")},
		wantErrs: []string{"gui.quick_menu[0].symbol"},
	}, {
		name:     "command is required",
		entries:  []any{map[string]any{"symbol": "plus"}},
		wantErrs: []string{"gui.quick_menu[0].command"},
	}, {
		name:     "command must not be empty",
		entries:  []any{quickMenuEntry("plus", "Empty", "   ")},
		wantErrs: []string{"gui.quick_menu[0].command"},
	}, {
		name:     "command elements must be strings",
		entries:  []any{quickMenuEntry("plus", "Bad", []any{"windownew", 2})},
		wantErrs: []string{"gui.quick_menu[0].command"},
	}, {
		name:     "command must not contain control characters",
		entries:  []any{quickMenuEntry("plus", "Bad", "help\x1b[2J")},
		wantErrs: []string{"gui.quick_menu[0].command"},
	}, {
		name:     "title must be a string",
		entries:  []any{map[string]any{"symbol": "plus", "command": "help", "title": 3}},
		wantErrs: []string{"gui.quick_menu[0].title"},
	}, {
		name: "title defaults to the command line",
		entries: []any{
			map[string]any{"symbol": "plus", "command": []any{"windownew", "right"}},
		},
		want: []QuickMenuButton{{Symbol: "plus", Title: "windownew right",
			Command: []string{"windownew", "right"}}},
	}, {
		name:     "unknown keys are rejected",
		entries:  []any{map[string]any{"symbol": "plus", "command": "help", "icon": "x"}},
		wantErrs: []string{"gui.quick_menu[0]"},
	}, {
		name: "duplicate command lines are rejected",
		entries: []any{
			quickMenuEntry("plus", "First", "help"),
			quickMenuEntry("xmark", "Second", "help"),
		},
		want: []QuickMenuButton{
			{Symbol: "plus", Title: "First", Command: []string{"help"}},
		},
		wantErrs: []string{"gui.quick_menu[1]"},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs := make(map[string]error)
			guiCfg := config.MapConfig(map[string]any{"quick_menu": tc.entries})
			got := parseQuickMenuButtons(guiCfg, errs)
			if tc.want == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.want, got)
			}
			for _, key := range tc.wantErrs {
				assert.Contains(t, errs, key)
			}
			if len(tc.wantErrs) == 0 {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestQuickMenuButtonsCapped(t *testing.T) {
	t.Parallel()
	entries := make([]any, 0, maxQuickMenuButtons+1)
	for i := range maxQuickMenuButtons + 1 {
		entries = append(entries,
			quickMenuEntry("plus", "Button", fmt.Sprintf("help %d", i)))
	}

	errs := make(map[string]error)
	got := parseQuickMenuButtons(
		config.MapConfig(map[string]any{"quick_menu": entries}), errs)
	assert.Len(t, got, maxQuickMenuButtons)
	assert.Contains(t, errs, "gui.quick_menu")
}

func TestQuickMenuButtonsMissingSection(t *testing.T) {
	t.Parallel()
	assert.Empty(t, QuickMenuButtons(config.MapConfig(map[string]any{})))
	assert.Empty(t, QuickMenuButtons(config.MapConfig(
		map[string]any{"gui": map[string]any{}})))
}

func TestValidateQuickMenuDropsInvalidEntries(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"gui": map[string]any{"quick_menu": []any{
		quickMenuEntry("plus", "Open Project", "workspaceopen"),
		quickMenuEntry("plus\x00", "Injected", "help"),
	}}}

	err := validateQuickMenu(cfg)
	assert.ErrorContains(t, err, "gui.quick_menu[1].symbol")

	// The invalid entry must be gone from the raw tree so it can never
	// reach the native bar.
	assert.Equal(t, []QuickMenuButton{{Symbol: "plus", Title: "Open Project",
		Command: []string{"workspaceopen"}}}, QuickMenuButtons(config.MapConfig(cfg)))
}

func TestValidateQuickMenuAcceptsValidConfig(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"gui": map[string]any{"quick_menu": []any{
		quickMenuEntry("plus", "Open Project", "workspaceopen"),
	}}}
	assert.NoError(t, validateQuickMenu(cfg))
	assert.NoError(t, validateQuickMenu(map[string]any{}))
	assert.NoError(t, validateQuickMenu(map[string]any{"gui": map[string]any{}}))
}

// TestFileExplorerMinWidthConfig pins the default the handler falls
// back to when editor.file_explorer is absent, so an unconfigured
// install still gets a visible explorer on an empty workspace.
func TestFileExplorerMinWidthConfig(t *testing.T) {
	t.Parallel()
	bare := ideConfig{cfg: map[string]any{}, errors: map[string]error{}}
	assert.Equal(t, 24, bare.fileExplorerMinWidth())
	assert.Empty(t, bare.errors)

	set := ideConfig{cfg: map[string]any{"editor": map[string]any{
		"file_explorer": map[string]any{"min_width": 40},
	}}, errors: map[string]error{}}
	assert.Equal(t, 40, set.fileExplorerMinWidth())
	assert.Empty(t, set.errors)
}

// TestFileExplorerReadOnlyConfigDefaults pins the defaults for the
// read-only knobs, so an install that never touches the block still
// gets an editable explorer with a named way into and out of it.
func TestFileExplorerReadOnlyConfigDefaults(t *testing.T) {
	t.Parallel()
	c := ideConfig{cfg: map[string]any{}, errors: map[string]error{}}

	assert.False(t, c.fileExplorerReadOnly())
	assert.Equal(t, term.KeyComb{Key: term.KeyEsc, Mod: term.ModShift},
		c.fileExplorerEditKey())
	assert.True(t, c.fileExplorerHint())
	assert.Equal(t, term.Attributes{Fg: term.ColorGray},
		c.fileExplorerHintAttr())
	assert.Empty(t, c.errors)
}

// TestFileExplorerReadOnlyConfigOverrides verifies every read-only
// knob is reachable from editor.file_explorer.
func TestFileExplorerReadOnlyConfigOverrides(t *testing.T) {
	t.Parallel()
	c := ideConfig{cfg: map[string]any{"editor": map[string]any{
		"file_explorer": map[string]any{
			"read_only": true,
			"edit_key":  "<c-e>",
			"hint":      false,
			"hint_attr": map[string]any{"fg": "blue"},
		},
	}}, errors: map[string]error{}}

	assert.True(t, c.fileExplorerReadOnly())
	assert.Equal(t, term.KeyComb{Ch: 'e', Mod: term.ModCtrl},
		c.fileExplorerEditKey())
	assert.False(t, c.fileExplorerHint())
	assert.Equal(t, term.Attributes{Fg: term.ColorBlue},
		c.fileExplorerHintAttr())
	assert.Empty(t, c.errors)
}

// TestFileExplorerEditKeyInvalid records the error and keeps the
// default rather than leaving the explorer with no way out of
// read-only.
func TestFileExplorerEditKeyInvalid(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"<nope>", "ab"} {
		c := ideConfig{cfg: map[string]any{"editor": map[string]any{
			"file_explorer": map[string]any{"edit_key": spec},
		}}, errors: map[string]error{}}

		assert.Equal(t, term.KeyComb{Key: term.KeyEsc, Mod: term.ModShift},
			c.fileExplorerEditKey())
		assert.Contains(t, c.errors, "editor.file_explorer.edit_key")
	}
}
