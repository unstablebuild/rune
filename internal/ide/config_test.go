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
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/imagecomp"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idestatus"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/sounds"
	"unstable.build/rune/internal/term/programstatus"
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
        hover_attr:
            bg: silver
            fg: navy
        clicked_attr:
            bg: purple
            fg: white
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
            graphics:
                topleft: 'file:///frames/tl.png'
                horizontaltop: 'https://example.com/top.png'
        scroll_bar_attr:
            fg: "#f0f0f0"
        scroll_bar_charset:
            top_char: '▲'
            middle_char: '|'
            hover_middle_char: 'X'
            graphics:
                mode: overlay
                middle_char: 'file:///frames/bar.png'
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
    wallpaper_ascii: abc
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
    bell: info
    modal: true
    dynamic_tab_name: true
    initial_reservoir: 0
    clipboard:
        write: false
        read: true
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
    status:
        enabled: true
        spinner: "abc"
        spinner_fps: 4
        icons:
            permission: "P"
            question: "Q"
            auth: "A"
            done: "D"
            error: "E"
        content_tab:
            progress: true
            idle_icon_attr:
                fg: green
            working_icon_attr:
                fg: teal
            working_done_attr:
                fg: lime
            blocked_attr:
                fg: olive
            blocked_icon_attr:
                fg: maroon
            done_attr:
                fg: navy
            done_icon_attr:
                fg: purple
            error_attr:
                fg: fuchsia
            error_icon_attr:
                fg: aqua
        workspace_tab:
            working_done_attr:
                fg: blue
            blocked_attr:
                fg: white
            error_icon_attr:
                fg: black
        notify:
          - blocked
          - error
        notify_interval: 5s
program_status:
    sounds:
        done: ""
        working: info
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
	assert.NotNil(t, vteConfig.Sounds)
	assert.NotNil(t, vteConfig.ScheduleNextTick)
	vteConfig.ScheduleNextTick = nil
	vteConfig.Sounds = nil
	assertTerminalSearchConfig(t, &vteConfig, cfg.standardResultAttr(), selectAttr)
	assert.Equal(t, vte.Config{
		Clipboard:                cfg.clipboard(),
		ClipboardAccess:          vte.ClipboardAccess{Write: true},
		SelectionAttributes:      selectAttr,
		NeedsAttentionAttributes: term.Attributes{Attrs: term.AttrBlink},
		Modal:                    true,
		ClipboardRegister:        clipboard.DefaultRegisterID,
		MaxLines:                 10_000,
		MinWidth:                 defaultMinWidth,
		BellSound:                sounds.Warning,
	}, vteConfig)
	assert.True(t, cfg.tabStatusEnabled())
	assert.Equal(t, browser.DefaultTabStatusConfig(), cfg.tabStatusConfig())
	assert.Equal(t, idestatus.DefaultNotifyConfig(), cfg.tabStatusNotifyConfig())
	assert.Equal(t, command.DefaultConfig().ShowManual, cfg.commandOverlayShowManual())
	assert.Equal(t, command.DefaultConfig().ManualAttr, cfg.commandOverlayManualAttr())
	assert.Zero(t, cfg.defaultAttr())

	assert.Equal(t, term.Attributes{Fg: term.ColorBlack, Bg: term.ColorYellow},
		cfg.standardResultAttr())
	assert.Equal(t, term.Attributes{}, cfg.standardAttr())
	assert.Equal(t, cfg.standardResultAttr(), cfg.emacsResultAttr())
	assert.Equal(t, term.Attributes{}, cfg.emacsMessageBarAttr())
	assert.Equal(t, term.Attributes{}, cfg.vimMessageBarAttr())
	assert.Equal(t, cfg.standardAttr(), cfg.emacsAttr())
	assert.Equal(t, term.Attributes{}, cfg.vimAttr())
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

	assert.Equal(t, "vim", cfg.editorMode())
	os.Setenv("SHELL", "fish")
}

func TestConfigDefault(t *testing.T) {
	ret := new(ideConfig)
	initDefaultConfig(ret, nil,
		sounds.Silent, term.ScheduleNextTick, "")
	assertDefaultConfig(t, ret)
}

func TestTabStatusConfigProgress(t *testing.T) {
	for _, key := range []string{"content_tab", "workspace_tab"} {
		for _, tc := range []struct {
			name    string
			status  map[string]any
			want    bool
			wantErr bool
		}{
			{name: "no status config"},
			{name: "missing", status: map[string]any{}},
			{name: "missing progress", status: map[string]any{key: map[string]any{}}},
			{name: "top level is ignored", status: map[string]any{"progress": true}},
			{name: "disabled", status: map[string]any{key: map[string]any{"progress": false}}},
			{name: "enabled", status: map[string]any{key: map[string]any{"progress": true}}, want: true},
			{name: "invalid", status: map[string]any{key: map[string]any{"progress": "true"}}, wantErr: true},
		} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				cfg := ideConfig{cfg: map[string]any{}, errors: map[string]error{}}
				if tc.status != nil {
					cfg.cfg["terminal"] = map[string]any{"status": tc.status}
				}
				got := cfg.tabStatusConfig()
				style, other := got.ContentTab, got.WorkspaceTab
				if key == "workspace_tab" {
					style, other = other, style
				}
				assert.Equal(t, tc.want, style.Progress)
				assert.False(t, other.Progress, "progress is set per kind of tab")
				assert.True(t, cfg.tabStatusEnabled())
				if tc.wantErr {
					assert.Error(t, cfg.errors["terminal.status."+key+".progress"])
				} else {
					assert.Empty(t, cfg.errors)
				}
			})
		}
	}
}

func TestStatusInboxStyle(t *testing.T) {
	def := idestatus.DefaultInboxStyle()
	attrKeys := []string{
		"marker_attr", "selected_attr", "hover_attr", "workspace_attr", "workspace_highlight_attr",
		"progress_attr", "progress_done_attr", "progress_remaining_attr", "progress_remaining_selected_attr",
		"progress_label_attr", "more_below_attr", "divider_attr", "placeholder_attr",
	}
	invalid := map[string]any{"marker": true, "progress_bar": true, "more_below": true}
	wantInvalid := []string{"program_status.marker", "program_status.progress_bar", "program_status.more_below"}
	for _, key := range attrKeys {
		invalid[key] = "gray"
		wantInvalid = append(wantInvalid, "program_status."+key)
	}
	for _, tc := range []struct {
		name    string
		cfg     map[string]any
		want    idestatus.InboxStyle
		wantErr []string
	}{
		{name: "defaults", cfg: map[string]any{}, want: def},
		{
			name: "the marker takes the color of the focused tab marker",
			cfg: map[string]any{"browser": map[string]any{
				"focus_tab_highlight_attr": map[string]any{"fg": "red", "bg": "navy"},
			}},
			want: func() idestatus.InboxStyle {
				s := def
				s.MarkerAttr = term.Attributes{Fg: term.ColorRed}
				return s
			}(),
		},
		{
			name: "the marker keeps what it does not set of the focused tab marker",
			cfg: map[string]any{
				"browser":        map[string]any{"focus_tab_highlight_attr": map[string]any{"fg": "red"}},
				"program_status": map[string]any{"marker_attr": map[string]any{"bg": "navy"}},
			},
			want: func() idestatus.InboxStyle {
				s := def
				s.MarkerAttr = term.Attributes{Fg: term.ColorRed, Bg: term.ColorNavy}
				return s
			}(),
		},
		{
			name: "configured",
			cfg: map[string]any{"program_status": map[string]any{
				"marker":                           "┃",
				"marker_attr":                      map[string]any{"fg": "purple", "bg": "red"},
				"selected_attr":                    map[string]any{"bg": "navy"},
				"hover_attr":                       map[string]any{"bg": "teal"},
				"workspace_attr":                   map[string]any{"fg": "blue"},
				"workspace_highlight_attr":         map[string]any{"fg": "black"},
				"progress_bar":                     "╢░_╟",
				"progress_attr":                    map[string]any{"bg": "maroon"},
				"progress_done_attr":               map[string]any{"fg": "lime"},
				"progress_remaining_attr":          map[string]any{"fg": "silver"},
				"progress_remaining_selected_attr": map[string]any{"fg": "white"},
				"progress_label_attr":              map[string]any{"fg": "aqua"},
				"more_below":                       "↓",
				"more_below_attr":                  map[string]any{"fg": "yellow"},
				"divider_attr":                     map[string]any{"fg": "olive"},
				"placeholder_attr":                 map[string]any{"fg": "fuchsia"},
			}},
			want: func() idestatus.InboxStyle {
				s := def
				s.Marker = '┃'
				s.MarkerAttr = term.Attributes{Fg: term.ColorPurple, Bg: term.ColorRed}
				s.SelectedAttr = term.Attributes{Bg: term.ColorNavy}
				s.HoverAttr = term.Attributes{Bg: term.ColorTeal}
				s.WorkspaceAttr = term.Attributes{Fg: term.ColorBlue}
				s.WorkspaceHighlightAttr = term.Attributes{Fg: term.ColorBlack}
				s.ProgressBar = component.ProgressBarCharSet{Left: '╢', Filled: '░', Empty: '_', Right: '╟'}
				s.ProgressAttr = term.Attributes{Bg: term.ColorMaroon}
				s.ProgressDoneAttr = term.Attributes{Fg: term.ColorLime}
				s.ProgressRemainingAttr = term.Attributes{Fg: term.ColorSilver}
				s.ProgressRemainingSelectedAttr = term.Attributes{Fg: term.ColorWhite}
				s.ProgressLabelAttr = term.Attributes{Fg: term.ColorAqua}
				s.MoreBelow = '↓'
				s.MoreBelowAttr = term.Attributes{Fg: term.ColorYellow}
				s.DividerAttr = term.Attributes{Fg: term.ColorOlive}
				s.PlaceholderAttr = term.Attributes{Fg: term.ColorFuchsia}
				return s
			}(),
		},
		{
			name: "the same character all along the bar",
			cfg:  map[string]any{"program_status": map[string]any{"progress_bar": "━━━━"}},
			want: func() idestatus.InboxStyle {
				s := def
				s.ProgressBar = component.ProgressBarCharSet{Left: '━', Filled: '━', Empty: '━', Right: '━'}
				return s
			}(),
		},
		{
			name:    "invalid",
			cfg:     map[string]any{"program_status": invalid},
			want:    def,
			wantErr: wantInvalid,
		},
		{
			name:    "a progress bar short of a character",
			cfg:     map[string]any{"program_status": map[string]any{"progress_bar": "╢░╟"}},
			want:    def,
			wantErr: []string{"program_status.progress_bar"},
		},
		{
			name:    "a progress bar of wide characters",
			cfg:     map[string]any{"program_status": map[string]any{"progress_bar": "界界界界"}},
			want:    def,
			wantErr: []string{"program_status.progress_bar"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ideConfig{cfg: tc.cfg, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.statusInboxStyle())
			assert.ElementsMatch(t, tc.wantErr, slices.Collect(maps.Keys(cfg.errors)))
		})
	}
}

func TestTabStatusConfigIgnoresPluginIcons(t *testing.T) {
	m, err := decodeConfig(strings.NewReader(`
terminal:
    plugin:
        status_error_icon: "X"
        status_success_icon: "$"
`))
	require.NoError(t, err)
	var cfg ideConfig
	initConfig(&cfg, m, nil, sounds.Silent, term.ScheduleNextTick, "")
	def := browser.DefaultTabStatusConfig()
	got := cfg.tabStatusConfig()
	assert.Equal(t, def.DoneIcon, got.DoneIcon)
	assert.Equal(t, def.ErrorIcon, got.ErrorIcon)
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

func TestTerminalClipboardAccess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal map[string]any
		want     vte.ClipboardAccess
		wantErr  string
	}{
		{
			name: "unset lets programs write but not read",
			want: vte.ClipboardAccess{Write: true},
		},
		{
			name:     "empty clipboard section keeps the defaults",
			terminal: map[string]any{"clipboard": map[string]any{}},
			want:     vte.ClipboardAccess{Write: true},
		},
		{
			name:     "read on",
			terminal: map[string]any{"clipboard": map[string]any{"read": true}},
			want:     vte.ClipboardAccess{Write: true, Read: true},
		},
		{
			name:     "write off",
			terminal: map[string]any{"clipboard": map[string]any{"write": false}},
			want:     vte.ClipboardAccess{},
		},
		{
			name:     "section is not a map",
			terminal: map[string]any{"clipboard": "yes"},
			want:     vte.ClipboardAccess{Write: true},
			wantErr:  "terminal.clipboard",
		},
		{
			name:     "read is not a bool",
			terminal: map[string]any{"clipboard": map[string]any{"read": "yes"}},
			want:     vte.ClipboardAccess{Write: true},
			wantErr:  "terminal.clipboard.read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.terminal != nil {
				m["terminal"] = tc.terminal
			}
			cfg := &ideConfig{cfg: m, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.terminalClipboardAccess())
			assert.Equal(t, tc.want, cfg.terminalConfig().ClipboardAccess)
			if tc.wantErr == "" {
				assert.Empty(t, cfg.errors)
				return
			}
			assert.Error(t, cfg.errors[tc.wantErr])
		})
	}
}

func TestTerminalBellSound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal map[string]any
		want     string
	}{
		{name: "unset plays a warning", want: sounds.Warning},
		{name: "named sound", terminal: map[string]any{"bell": "ding"}, want: "ding"},
		{name: "empty plays nothing", terminal: map[string]any{"bell": ""}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.terminal != nil {
				m["terminal"] = tc.terminal
			}
			cfg := &ideConfig{cfg: m, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.terminalConfig().BellSound)
			assert.Empty(t, cfg.errors)
		})
	}
}

func TestValidateSounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        map[string]any
		wantSounds map[string]any
		wantBell   any
		// wantStatus is what is left of program_status.sounds.
		wantStatus any
		// wantQuickMenu is what is left of gui.quick_menu_sounds.
		wantQuickMenu any
		wantErrs      []string
	}{
		{
			name:     "no sounds and a reserved bell",
			cfg:      map[string]any{"terminal": map[string]any{"bell": "info"}},
			wantBell: "info",
		},
		{
			name: "bell names a configured sound",
			cfg: map[string]any{
				"sounds":   map[string]any{"ding": "/ding.wav", "chime": "ding"},
				"terminal": map[string]any{"bell": "chime"},
			},
			wantSounds: map[string]any{"ding": "/ding.wav", "chime": "ding"},
			wantBell:   "chime",
		},
		{
			name:     "empty bell is silent",
			cfg:      map[string]any{"terminal": map[string]any{"bell": ""}},
			wantBell: "",
		},
		{
			name: "invalid entries are dropped",
			cfg: map[string]any{"sounds": map[string]any{
				"ok": "/ok.wav", "typo": "warnign", "number": 3,
			}},
			wantSounds: map[string]any{"ok": "/ok.wav"},
			wantErrs: []string{`'sounds.number' must be a file path`,
				`sound "typo": refers to unknown sound "warnign"`},
		},
		{
			name:       "sounds is not a map",
			cfg:        map[string]any{"sounds": "ding.wav"},
			wantSounds: map[string]any{},
			wantErrs:   []string{`'sounds' must map sound names`},
		},
		{
			name: "unknown bell resets to warning",
			cfg: map[string]any{
				"sounds":   map[string]any{"typo": "warnign"},
				"terminal": map[string]any{"bell": "typo"},
			},
			wantSounds: map[string]any{},
			wantBell:   sounds.Warning,
			wantErrs:   []string{`'terminal.bell' names unknown sound "typo"`},
		},
		{
			name:     "bell of the wrong type resets to warning",
			cfg:      map[string]any{"terminal": map[string]any{"bell": false}},
			wantBell: sounds.Warning,
			wantErrs: []string{`'terminal.bell' must be a sound name or "", got bool`},
		},
		{
			name: "program status sounds name states and known sounds",
			cfg: map[string]any{
				"sounds": map[string]any{"chime": "/chime.wav"},
				"program_status": map[string]any{"sounds": map[string]any{
					"done": "chime", "error": "", "working": "info",
				}},
			},
			wantSounds: map[string]any{"chime": "/chime.wav"},
			wantStatus: map[string]any{"done": "chime", "error": "", "working": "info"},
		},
		{
			name: "invalid program status sounds are dropped",
			cfg: map[string]any{"program_status": map[string]any{"sounds": map[string]any{
				"done": "chmie", "idle": "info", "blocked": 1, "error": "error",
			}}},
			wantStatus: map[string]any{"error": "error"},
			wantErrs: []string{
				`'program_status.sounds.done' names unknown sound "chmie"`,
				`'program_status.sounds.idle': unknown state`,
				`'program_status.sounds.blocked' must be a sound name or "", got int`,
			},
		},
		{
			name:       "program status sounds is not a map",
			cfg:        map[string]any{"program_status": map[string]any{"sounds": "chime"}},
			wantStatus: nil,
			wantErrs:   []string{`'program_status.sounds' must map states to sound names`},
		},
		{
			name: "quick menu sounds name events and known sounds",
			cfg: map[string]any{
				"sounds": map[string]any{"tick": "/tick.wav"},
				"gui": map[string]any{"quick_menu_sounds": map[string]any{
					"hover": "tick", "click": "",
				}},
			},
			wantSounds:    map[string]any{"tick": "/tick.wav"},
			wantQuickMenu: map[string]any{"hover": "tick", "click": ""},
		},
		{
			name: "invalid quick menu sounds are dropped",
			cfg: map[string]any{"gui": map[string]any{"quick_menu_sounds": map[string]any{
				"hover": "tikc", "click": 1, "press": "info",
			}}},
			wantQuickMenu: map[string]any{},
			wantErrs: []string{
				`'gui.quick_menu_sounds.hover' names unknown sound "tikc"`,
				`'gui.quick_menu_sounds.click' must be a sound name or "", got int`,
				`'gui.quick_menu_sounds.press': unknown event`,
			},
		},
		{
			name:          "quick menu sounds is not a map",
			cfg:           map[string]any{"gui": map[string]any{"quick_menu_sounds": "info"}},
			wantQuickMenu: nil,
			wantErrs:      []string{`'gui.quick_menu_sounds' must map hover and click`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSounds(tc.cfg)
			if len(tc.wantErrs) == 0 {
				require.NoError(t, err)
			}
			for _, want := range tc.wantErrs {
				assert.ErrorContains(t, err, want)
			}
			if tc.wantSounds == nil {
				assert.NotContains(t, tc.cfg, "sounds")
			} else {
				assert.Equal(t, tc.wantSounds, tc.cfg["sounds"])
			}
			if terminal, ok := tc.cfg["terminal"].(map[string]any); ok {
				assert.Equal(t, tc.wantBell, terminal["bell"])
			}
			if status, ok := tc.cfg["program_status"].(map[string]any); ok {
				assert.Equal(t, tc.wantStatus, status["sounds"])
			}
			if gui, ok := tc.cfg["gui"].(map[string]any); ok {
				assert.Equal(t, tc.wantQuickMenu, gui["quick_menu_sounds"])
			}
		})
	}
}

func TestSoundEntries(t *testing.T) {
	t.Setenv("RUNE_TEST_SOUNDS", "/home/rune/sounds")
	cfg := &ideConfig{cfg: map[string]any{"sounds": map[string]any{
		"pkg":     "$RUNE_DATADIR/lib/pkg/ding.wav",
		"env":     "$RUNE_TEST_SOUNDS/chime.ogg",
		"uri":     "file:///opt/sounds/pop.mp3",
		"alias":   "pkg",
		"silent":  "",
		"ignored": 3,
	}}, errors: map[string]error{}}
	assert.Equal(t, map[string]string{
		"pkg":    "/data/lib/pkg/ding.wav",
		"env":    "/home/rune/sounds/chime.ogg",
		"uri":    "/opt/sounds/pop.mp3",
		"alias":  "pkg",
		"silent": "",
	}, cfg.soundEntries("/data"))
}

func TestApplySounds(t *testing.T) {
	var played []sounds.Sound
	registry := sounds.NewRegistry(sounds.BackendFunc(func(s sounds.Sound) {
		played = append(played, s)
	}))
	cfg := &ideConfig{cfg: map[string]any{"sounds": map[string]any{
		"ding":         "$RUNE_DATADIR/ding.wav",
		sounds.Warning: "ding",
	}}, errors: map[string]error{}}
	cfg.applySounds(registry, "/data")
	require.Empty(t, cfg.errors)

	registry.Play(sounds.Warning)
	assert.Equal(t, []sounds.Sound{
		{Name: sounds.Warning, Path: "/data/ding.wav", BuiltIn: sounds.Warning},
	}, played)
}

func TestTerminalModalDefaultFromEditorMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		editor map[string]any
		want   bool
	}{
		{"unset editor defaults modal", nil, true},
		{"vim", map[string]any{"mode": "vim"}, true},
		{"deprecated modal", map[string]any{"mode": "modal"}, true},
		{"modeless", map[string]any{"mode": "modeless"}, false},
		{"exo fallback vim", map[string]any{
			"mode": "exo",
			"exo":  map[string]any{"command": "vim {file}", "fallback": "vim"},
		}, true},
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
		"editor":   map[string]any{"mode": "vim"},
		"terminal": map[string]any{"modal": false},
	}, errors: map[string]error{}}
	assert.False(t, cfg.terminalModal())
}

func TestEditorModeNormalizesDeprecatedAliases(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"modeless", editorModeStandard},
		{"standard", editorModeStandard},
		{"emacs", editorModeEmacs},
		{"vim", editorModeVim},
		{"modal", editorModeVim},
		{"helix", editorModeHelix},
		{"exo", editorModeExo},
		{"bogus", editorModeVim},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"editor": map[string]any{"mode": tc.mode},
			}, errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.editorMode())
			assert.Empty(t, cfg.errors, "a deprecated alias is not a config error")
		})
	}
}

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
		{editorModeVim, true},
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

	// env vars expand at the config boundary; file APIs keep "$" literal
	t.Setenv("RUNE_TEST_WORKSPACE_HOME", "/srv/work")
	cfg = &ideConfig{cfg: map[string]any{
		"workspace": map[string]any{"home": "$RUNE_TEST_WORKSPACE_HOME/src"},
	}, errors: map[string]error{}}
	assert.Equal(t, "/srv/work/src", cfg.workspaceHome())

	// expanding to empty → default "~"
	cfg = &ideConfig{cfg: map[string]any{
		"workspace": map[string]any{"home": "$RUNE_TEST_UNSET_WORKSPACE_HOME"},
	}, errors: map[string]error{}}
	assert.Equal(t, "~", cfg.workspaceHome())
}

func TestLogOutputPathExpandsEnv(t *testing.T) {
	t.Setenv("RUNE_TEST_LOG_DIR", "/var/log/rune")
	cfg := &ideConfig{cfg: map[string]any{
		"log_path": "$RUNE_TEST_LOG_DIR/debug.log",
	}, errors: map[string]error{}}
	assert.Equal(t, "/var/log/rune/debug.log", cfg.logOutputPath())
	assert.Empty(t, cfg.errors)
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
			"wallpaper_ascii": "notEmpty",
		},
	}
	ret.sounds = sounds.Silent
	ret.scheduleNextTick = term.ScheduleNextTick
	err = loadFileConfig(&ret, f.Name())
	assert.Error(t, err)
	assertDefaultConfig(t, &ret)
}

func TestConfigSetting(t *testing.T) {
	m, err := decodeConfig(strings.NewReader(sampleConfig))
	require.NoError(t, err)

	var cfg ideConfig
	initConfig(&cfg, m, nil,
		sounds.Silent, term.ScheduleNextTick, "")
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

	frameCharSet := component.FrameCharSetHighlight()
	frameCharSet.Graphics.TopLeft = "file:///frames/tl.png"
	frameCharSet.Graphics.HorizontalTop = "https://example.com/top.png"
	expectedConfig := handler.WindowManagerConfig{
		WindowManagerConfig: tcomponent.WindowManagerConfig{
			Frame:         true,
			FrameAttr:     term.Attributes{Fg: term.ColorRed},
			FrameCharSet:  frameCharSet,
			ScrollBarAttr: term.Attributes{Fg: term.GetColor("#f0f0f0")},
			ScrollBarCharSet: component.ScrollBarCharSet{
				Top: '▲', Middle: '|',
				Graphics: component.ScrollBarCharSetGraphics{
					Middle: "file:///frames/bar.png", Overlay: true,
				},
			},
			NoMaxSize: true,
			WindowBar: true,
			WindowBarCharSet: tcomponent.WindowBarCharSet{
				Left:       '▓',
				Horizontal: '▒',
				Right:      '░',
			},
			CloseIcon:     'x',
			CloseIconAttr: term.Attributes{Fg: term.ColorYellow},
		},
		Dim: false,
		ScrollBarHoverCharSet: component.ScrollBarCharSet{
			Middle:   'X',
			Graphics: component.ScrollBarCharSetGraphics{Overlay: true},
		},
		FocusFrameAttr:    handler.DefaultWindowManagerConfig().FrameAttr,
		FocusFrameCharSet: handler.DefaultWindowManagerConfig().FrameCharSet,
	}
	wmConfig := cfg.windowManagerConfig()
	// Frame images are cached in a partition of their own.
	require.NotNil(t, wmConfig.FrameGraphicsStorage)
	require.NoError(t, wmConfig.FrameGraphicsStorage.Set(context.Background(), "frame", map[string]any{"a": 1}))
	frameImages, err := cfg.storage.Partition("frame_images")
	require.NoError(t, err)
	var frameDoc map[string]any
	require.NoError(t, frameImages.Get(context.Background(), "frame", &frameDoc))
	assert.ErrorIs(t, cfg.storage.Get(context.Background(), "frame", &frameDoc), storageapi.ErrNotFound)
	wmConfig.FrameGraphicsStorage = nil
	assert.Equal(t, expectedConfig, wmConfig)
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
	}, cfg.vimMessageBarLayout())
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
	expectedFUCs.Graphics = frameCharSet.Graphics
	assert.Equal(t, expectedFUCs, cfg.frameUnionCharset())

	statusBar := cfg.statusBarEnabled()
	assert.True(t, statusBar)

	statusBarCfg := cfg.statusBarConfig(workspaceapi.URI{}, nil, nil, nil)
	assert.NotNil(t, statusBarCfg.ScheduleNextTick)
	statusBarCfg.ScheduleNextTick = nil
	// Images are cached in a partition of their own.
	require.NotNil(t, statusBarCfg.Storage)
	require.NoError(t, statusBarCfg.Storage.Set(context.Background(), "image", map[string]any{"a": 1}))
	images, err := cfg.storage.Partition("status_bar_images")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, images.Get(context.Background(), "image", &doc))
	assert.ErrorIs(t, cfg.storage.Get(context.Background(), "image", &doc), storageapi.ErrNotFound)
	statusBarCfg.Storage = nil
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
	assert.NotNil(t, vteConfig.Sounds)
	vteConfig.Sounds = nil
	assertTerminalSearchConfig(t, &vteConfig, cfg.standardResultAttr(),
		term.Attributes{Fg: term.ColorGreen, Bg: term.ColorTeal})
	expectedEmulatorConfig := vte.Config{
		CommandAndArgs:           []string{"sh"},
		Attributes:               term.Attributes{Fg: term.ColorWhite, Bg: term.ColorYellow},
		Clipboard:                cfg.clipboard(),
		ClipboardRegister:        clipboard.DefaultRegisterID,
		ClipboardAccess:          vte.ClipboardAccess{Read: true},
		SelectionAttributes:      term.Attributes{Fg: term.ColorGreen, Bg: term.ColorTeal},
		NeedsAttentionAttributes: term.Attributes{Attrs: term.AttrBlink, Fg: term.ColorRed},
		Modal:                    true,
		DynamicTabName:           true,
		MaxLines:                 999,
		Bell:                     []byte{0x07},
		BellSound:                sounds.Info,
		MinWidth:                 defaultMinWidth,
	}
	assert.Equal(t, expectedEmulatorConfig, vteConfig)

	assert.True(t, cfg.tabStatusEnabled())
	assert.Equal(t, browser.TabStatusConfig{
		Spinner:    []rune("abc"),
		SpinnerFPS: 4,
		BlockedIcons: map[programstatus.Kind]rune{
			programstatus.KindNone:       'P',
			programstatus.KindPermission: 'P',
			programstatus.KindQuestion:   'Q',
			programstatus.KindAuth:       'A',
		},
		DoneIcon:  'D',
		ErrorIcon: 'E',
		ContentTab: browser.TabStatusStyle{
			Progress:        true,
			IdleIconAttr:    term.Attributes{Fg: term.ColorGreen},
			WorkingIconAttr: term.Attributes{Fg: term.ColorTeal},
			WorkingDoneAttr: term.Attributes{Fg: term.ColorLime},
			BlockedAttr:     term.Attributes{Fg: term.ColorOlive},
			BlockedIconAttr: term.Attributes{Fg: term.ColorMaroon},
			DoneAttr:        term.Attributes{Fg: term.ColorNavy},
			DoneIconAttr:    term.Attributes{Fg: term.ColorPurple},
			ErrorAttr:       term.Attributes{Fg: term.ColorFuchsia},
			ErrorIconAttr:   term.Attributes{Fg: term.ColorAqua},
		},
		WorkspaceTab: func() browser.TabStatusStyle {
			style := browser.DefaultTabStatusConfig().WorkspaceTab
			style.WorkingDoneAttr = term.Attributes{Fg: term.ColorBlue}
			style.BlockedAttr = term.Attributes{Fg: term.ColorWhite}
			style.ErrorIconAttr = term.Attributes{Fg: term.ColorBlack}
			return style
		}(),
	}, cfg.tabStatusConfig())
	assert.Equal(t, idestatus.NotifyConfig{
		States: map[programstatus.State]bool{
			programstatus.Blocked: true,
			programstatus.Error:   true,
		},
		Interval: 5 * time.Second,
		Messages: true,
		Sounds: map[programstatus.State]string{
			programstatus.Blocked: sounds.Warning,
			programstatus.Error:   sounds.Error,
			programstatus.Done:    "",
			programstatus.Working: sounds.Info,
		},
	}, cfg.tabStatusNotifyConfig())

	expectedPrompt := browser.PromptConfig{
		TextAttr:      term.Attributes{Fg: term.ColorTeal},
		HighlightAttr: term.Attributes{Fg: term.GetColor("#f0f0f0"), Bg: term.ColorRed},
		HoverAttr:     term.Attributes{Fg: term.ColorNavy, Bg: term.ColorSilver},
		ClickedAttr:   term.Attributes{Fg: term.ColorWhite, Bg: term.ColorPurple},
		MinWidth:      browser.DefaultConfig().MinWidth,
	}
	assert.Equal(t, expectedPrompt, cfg.promptConfig())

	assert.Equal(t, term.Attributes{Bg: term.ColorRed,
		Fg: term.GetColor("#f0f0f0")}, cfg.vimResultAttr())

	assert.Equal(t, term.Attributes{Bg: term.ColorRed,
		Fg: term.GetColor("#f1f1f1")}, cfg.standardResultAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorMaroon,
		Fg: term.GetColor("#f7f7f7")}, cfg.emacsResultAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorTeal,
		Fg: term.ColorWhite}, cfg.emacsMessageBarAttr())
	assert.Equal(t, term.Attributes{Bg: term.ColorNavy,
		Fg: term.ColorSilver}, cfg.vimMessageBarAttr())

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
		Fg: term.GetColor("#f2f2f2")}, cfg.vimAttr())

	assert.Equal(t, "vim", cfg.editorMode())
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
	cfg.cfg["workspace"].(map[string]any)["wallpaper_ascii"] = ""
	extensionCfgStruct.parent.cfg["workspace"].(map[string]any)["wallpaper_ascii"] = ""
	cfg.sounds = nil
	cfg.scheduleNextTick = nil
	extensionCfgStruct.parent.sounds = nil
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

func TestConfigImageWallpapersWithAnEmptyRampUseTheDefaultRamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "white.png")
	white := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	white.Set(0, 0, color.White)
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, white))
	require.NoError(t, f.Close())

	for _, key := range []string{"wallpaper_image", "wallpaper_image_ascii"} {
		t.Run(key, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"workspace": map[string]any{
					key:                            path,
					"wallpaper_density_characters": "",
				},
			}, errors: map[string]error{}}

			wallpaper := cfg.wallpaper()
			assert.Contains(t, cfg.errors, "workspace.wallpaper_density_characters")

			const width, height = 20, 4
			art := wallpaper.NewComponent()
			art.Resize(width, height)
			w := cell.NewBufferWriter(context.Background(), width, height)
			require.NotPanics(t, func() { art.Draw(w) })
			var drawn []rune
			for _, row := range w.RawCells() {
				for _, c := range row {
					if c.Ch != 0 && c.Ch != ' ' {
						drawn = append(drawn, c.Ch)
					}
				}
			}
			require.NotEmpty(t, drawn)
			assert.Equal(t, strings.Repeat("▓", len(drawn)), string(drawn))
		})
	}
}

func TestConfigWallpaperResolvesTheImagesThenTheTextThenTheLogo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "white.png")
	white := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	white.Set(0, 0, color.White)
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, white))
	require.NoError(t, f.Close())
	largePath := filepath.Join(t.TempDir(), "large.png")
	f, err = os.Create(largePath)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, image.NewGray(image.Rect(0, 0, maxWallpaperImageSize+1, 1))))
	require.NoError(t, f.Close())

	logo := func(text string, fps int) Logo {
		return Logo{
			Wallpaper: browser.Wallpaper{NewComponent: func() tui.Component {
				return component.NewString(text)
			}},
			ShaderFPS: fps,
		}
	}
	logoA := logo("logo a", 1)

	cases := []struct {
		name      string
		workspace map[string]any
		logo      *Logo
		wantDrawn string
		// wantPicture are the cells of the pictures drawn, on cells of
		// 10x20 pixels.
		wantPicture []image.Rectangle
		wantFPS     int
		wantErrs    map[string]string
	}{
		{
			name:      "no wallpaper",
			workspace: map[string]any{},
		},
		{
			name:      "the logo",
			workspace: map[string]any{},
			logo:      &logoA,
			wantDrawn: "logoa",
			wantFPS:   1,
		},
		{
			name:      "the text over the logo",
			workspace: map[string]any{"wallpaper_ascii": "art"},
			logo:      &logoA,
			wantDrawn: "art",
		},
		{
			name:      "the text of the deprecated key",
			workspace: map[string]any{"wallpaper": "art"},
			logo:      &logoA,
			wantDrawn: "art",
		},
		{
			name: "the text over the text of the deprecated key",
			workspace: map[string]any{
				"wallpaper_ascii": "new", "wallpaper": "old",
			},
			logo:      &logoA,
			wantDrawn: "new",
		},
		{
			name: "the deprecated key under an empty text",
			workspace: map[string]any{
				"wallpaper_ascii": "", "wallpaper": "old",
			},
			logo:      &logoA,
			wantDrawn: "old",
		},
		{
			name: "the picture of an image over the text",
			workspace: map[string]any{
				"wallpaper_image": path, "wallpaper_ascii": "art",
			},
			logo:        &logoA,
			wantPicture: []image.Rectangle{image.Rect(8, 1, 12, 3)},
		},
		{
			name: "the picture of a section",
			workspace: map[string]any{
				"wallpaper_image": map[string]any{
					"path": path, "width": "100%", "height": 2, "fit": "fill",
				},
			},
			wantPicture: []image.Rectangle{image.Rect(0, 1, 20, 3)},
		},
		{
			name: "the picture over the ASCII art of an image",
			workspace: map[string]any{
				"wallpaper_image": path, "wallpaper_image_ascii": path,
			},
			wantPicture: []image.Rectangle{image.Rect(8, 1, 12, 3)},
		},
		{
			name: "the ASCII art of an image over the text",
			workspace: map[string]any{
				"wallpaper_image":       map[string]any{"path": ""},
				"wallpaper_image_ascii": path,
				"wallpaper_ascii":       "art",
			},
			wantDrawn: "▓",
		},
		{
			name:      "an image that is neither a path nor a section",
			workspace: map[string]any{"wallpaper_image": 42, "wallpaper_ascii": "art"},
			wantDrawn: "art",
			wantErrs: map[string]string{
				"workspace.wallpaper_image": "must be the path of an image or a section with its path",
			},
		},
		{
			name:      "an image that cannot be opened",
			workspace: map[string]any{"wallpaper_image_ascii": "/nonexistent.png", "wallpaper_ascii": "art"},
			wantDrawn: "art",
			wantErrs: map[string]string{
				"workspace.wallpaper_image_ascii": `open image "/nonexistent.png": ` +
					"open /nonexistent.png: no such file or directory",
			},
		},
		{
			name:      "a picture that cannot be opened",
			workspace: map[string]any{"wallpaper_image": "/nonexistent.png", "wallpaper_ascii": "art"},
			wantDrawn: "art",
			wantErrs: map[string]string{
				"workspace.wallpaper_image": `open image "/nonexistent.png": ` +
					"open /nonexistent.png: no such file or directory",
			},
		},
		{
			name:      "a picture too large to draw",
			workspace: map[string]any{"wallpaper_image": largePath, "wallpaper_ascii": "art"},
			wantDrawn: "art",
			wantErrs: map[string]string{
				"workspace.wallpaper_image": "image is 8193x1, larger than 8192x8192",
			},
		},
		{
			name: "empty values show the logo",
			workspace: map[string]any{
				"wallpaper_image": "", "wallpaper_image_ascii": "", "wallpaper_ascii": "",
				"wallpaper": "",
			},
			logo:      &logoA,
			wantDrawn: "logoa",
			wantFPS:   1,
		},
		{
			name: "empty values without a logo leave the window blank",
			workspace: map[string]any{
				"wallpaper_image": "", "wallpaper_image_ascii": "", "wallpaper_ascii": "",
				"wallpaper": "",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &ideConfig{
				cfg:    map[string]any{"workspace": tc.workspace},
				errors: map[string]error{},
				logo:   tc.logo,
			}

			const width, height = 20, 4
			wallpaper := cfg.wallpaper()
			comp := wallpaper.NewComponent()
			comp.Resize(width, height)
			ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 10, Height: 20})
			w := cell.NewBufferWriter(ctx, width, height)
			comp.Draw(w)
			// Collapses repeats so the cells of the image read as one.
			var drawn []rune
			for _, row := range w.RawCells() {
				for _, c := range row {
					if c.Ch != 0 && c.Ch != ' ' && (len(drawn) == 0 || drawn[len(drawn)-1] != c.Ch) {
						drawn = append(drawn, c.Ch)
					}
				}
			}
			assert.Equal(t, tc.wantDrawn, string(drawn))
			var pictures []image.Rectangle
			for _, img := range w.Images() {
				pictures = append(pictures, img.Visible())
			}
			assert.Equal(t, tc.wantPicture, pictures)
			assert.Equal(t, tc.wantFPS, cfg.intro().fps)
			errs := map[string]string{}
			for key, err := range cfg.errors {
				errs[key] = err.Error()
			}
			if tc.wantErrs == nil {
				tc.wantErrs = map[string]string{}
			}
			assert.Equal(t, tc.wantErrs, errs)
		})
	}
}

func TestConfigWallpaperImageSection(t *testing.T) {
	const lengthErr = `must be a number of cells, such as 40, or a percentage of the ` +
		`window from 1% to 100%, such as "60%"`
	cases := []struct {
		name     string
		section  map[string]any
		want     imagecomp.Config
		wantErrs map[string]string
	}{
		{
			name:    "the defaults",
			section: map[string]any{},
			want:    defaultWallpaperImage,
		},
		{
			name:    "cells and percentages",
			section: map[string]any{"width": 40, "height": "100%", "fit": "cover"},
			want: imagecomp.Config{
				Width:  imagecomp.Length{N: 40},
				Height: imagecomp.Length{N: 100, Percent: true},
				Fit:    imagecomp.Cover,
			},
		},
		{
			name:    "cells written as a string",
			section: map[string]any{"width": "40", "height": int64(10), "fit": "fill"},
			want: imagecomp.Config{
				Width:  imagecomp.Length{N: 40},
				Height: imagecomp.Length{N: 10},
				Fit:    imagecomp.Fill,
			},
		},
		{
			name:    "invalid values keep the defaults",
			section: map[string]any{"width": "150%", "height": 0, "fit": "stretch"},
			want:    defaultWallpaperImage,
			wantErrs: map[string]string{
				"workspace.wallpaper_image.width":  lengthErr,
				"workspace.wallpaper_image.height": lengthErr,
				"workspace.wallpaper_image.fit": `unknown fit "stretch", ` +
					"expected one of: contain, cover, fill",
			},
		},
		{
			name:    "values of the wrong type keep the defaults",
			section: map[string]any{"width": true, "fit": 1},
			want:    defaultWallpaperImage,
			wantErrs: map[string]string{
				"workspace.wallpaper_image.width": lengthErr,
				"workspace.wallpaper_image.fit":   "type is invalid",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &ideConfig{errors: map[string]error{}}
			assert.Equal(t, tc.want, cfg.wallpaperImageConfig(config.MapConfig(tc.section)))
			errs := map[string]string{}
			for key, err := range cfg.errors {
				errs[key] = err.Error()
			}
			if tc.wantErrs == nil {
				tc.wantErrs = map[string]string{}
			}
			assert.Equal(t, tc.wantErrs, errs)
		})
	}
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

func TestConfigWindowFrameGraphics(t *testing.T) {
	def := handler.DefaultWindowManagerConfig()
	cases := []struct {
		name      string
		key       string
		graphics  map[string]any
		want      component.FrameCharSetGraphics
		wantError string
	}{
		{
			name:     "frame charset",
			key:      "frame_charset",
			graphics: map[string]any{"topleft": "file:///frames/tl.png"},
			want:     component.FrameCharSetGraphics{TopLeft: "file:///frames/tl.png"},
		},
		{
			name:     "focus frame charset",
			key:      "focus_frame_charset",
			graphics: map[string]any{"verticalright": "https://example.com/right.png"},
			want:     component.FrameCharSetGraphics{VerticalRight: "https://example.com/right.png"},
		},
		{
			name:      "an invalid URI keeps the default",
			key:       "frame_charset",
			graphics:  map[string]any{"topleft": "ftp://example.com/tl.png"},
			wantError: `graphics.topleft: imageuri: unsupported scheme "ftp"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"browser": map[string]any{
					"window_manager": map[string]any{
						tc.key: map[string]any{"topleft": "╭", "graphics": tc.graphics},
					},
				},
			}, errors: map[string]error{}}

			wmCfg := cfg.windowManagerConfig()
			cs := wmCfg.FrameCharSet
			if tc.key == "focus_frame_charset" {
				cs = wmCfg.FocusFrameCharSet
			}
			assert.Equal(t, tc.want, cs.Graphics)
			assert.Nil(t, wmCfg.FrameGraphicsStorage, "no storage without ideConfig.storage")
			errKey := "window_manager." + tc.key
			if tc.wantError == "" {
				assert.Equal(t, '╭', cs.TopLeft)
				assert.NotContains(t, cfg.errors, errKey)
				return
			}
			assert.Equal(t, def.FrameCharSet, cs)
			assert.ErrorContains(t, cfg.errors[errKey], tc.wantError)
		})
	}
}

func TestConfigWindowScrollBarCharset(t *testing.T) {
	every := map[string]any{
		"top_char": "▗", "middle_char": "▐", "bottom_char": "▝",
		"hover_top_char": "▄", "hover_middle_char": "█", "hover_bottom_char": "▀",
		"graphics": map[string]any{
			"mode":              "replace",
			"top_char":          "file:///bar/top.png",
			"middle_char":       "file:///bar/middle.png",
			"bottom_char":       "file:///bar/bottom.png",
			"hover_top_char":    "file:///bar/hover-top.png",
			"hover_middle_char": "file:///bar/hover-middle.png",
			"hover_bottom_char": "file:///bar/hover-bottom.png",
		},
	}
	cases := []struct {
		name       string
		wm         map[string]any
		want       component.ScrollBarCharSet
		wantHover  component.ScrollBarCharSet
		wantOutset int
		wantError  map[string]string
	}{
		{
			name: "unset is the frame's right edge",
			wm:   map[string]any{},
		},
		{
			name: "every key",
			wm:   map[string]any{"scroll_bar_charset": every},
			want: component.ScrollBarCharSet{
				Top: '▗', Middle: '▐', Bottom: '▝',
				Graphics: component.ScrollBarCharSetGraphics{
					Top:    "file:///bar/top.png",
					Middle: "file:///bar/middle.png",
					Bottom: "file:///bar/bottom.png",
				},
			},
			wantHover: component.ScrollBarCharSet{
				Top: '▄', Middle: '█', Bottom: '▀',
				Graphics: component.ScrollBarCharSetGraphics{
					Top:    "file:///bar/hover-top.png",
					Middle: "file:///bar/hover-middle.png",
					Bottom: "file:///bar/hover-bottom.png",
				},
			},
		},
		{
			name: "the older keys are the middle characters",
			wm: map[string]any{
				"scroll_bar_char": "|", "scroll_bar_hover_char": "X",
			},
			want:      component.ScrollBarCharSet{Middle: '|'},
			wantHover: component.ScrollBarCharSet{Middle: 'X'},
		},
		{
			name: "the older keys win over the charset",
			wm: map[string]any{
				"scroll_bar_char":    "|",
				"scroll_bar_charset": map[string]any{"middle_char": "▐", "top_char": "▗"},
			},
			want: component.ScrollBarCharSet{Top: '▗', Middle: '|'},
		},
		{
			name: "the hover graphics share the mode",
			wm: map[string]any{"scroll_bar_charset": map[string]any{
				"graphics": map[string]any{
					"mode": "overlay", "hover_middle_char": "file:///bar/hover.png",
				},
			}},
			want: component.ScrollBarCharSet{
				Graphics: component.ScrollBarCharSetGraphics{Overlay: true},
			},
			wantHover: component.ScrollBarCharSet{
				Graphics: component.ScrollBarCharSetGraphics{
					Middle: "file:///bar/hover.png", Overlay: true,
				},
			},
		},
		{
			name: "an invalid character is reported and skipped",
			wm: map[string]any{"scroll_bar_charset": map[string]any{
				"middle_char": true, "bottom_char": "▝",
			}},
			want: component.ScrollBarCharSet{Bottom: '▝'},
			wantError: map[string]string{
				"window_manager.scroll_bar_charset.middle_char": "",
			},
		},
		{
			name: "invalid graphics keep the characters",
			wm: map[string]any{"scroll_bar_charset": map[string]any{
				"middle_char": "▐",
				"graphics": map[string]any{
					"middle_char":       "file:///bar/middle.png",
					"hover_middle_char": "ftp://example.com/hover.png",
				},
			}},
			want: component.ScrollBarCharSet{Middle: '▐'},
			wantError: map[string]string{
				"window_manager.scroll_bar_charset": `graphics.hover_middle_char: imageuri: unsupported scheme "ftp"`,
			},
		},
		{
			name: "an outset stretches the images left",
			wm: map[string]any{"scroll_bar_charset": map[string]any{
				"graphics": map[string]any{
					"middle_char": "file:///bar/middle.png",
					"outset":      map[string]any{"left": 2},
				},
			}},
			want: component.ScrollBarCharSet{
				Graphics: component.ScrollBarCharSetGraphics{Middle: "file:///bar/middle.png"},
			},
			wantOutset: 2,
		},
		{
			name: "a negative outset is reported and skipped",
			wm: map[string]any{"scroll_bar_charset": map[string]any{
				"graphics": map[string]any{
					"middle_char": "file:///bar/middle.png",
					"outset":      map[string]any{"left": -1},
				},
			}},
			want: component.ScrollBarCharSet{
				Graphics: component.ScrollBarCharSetGraphics{Middle: "file:///bar/middle.png"},
			},
			wantError: map[string]string{
				"window_manager.scroll_bar_charset.graphics.outset.left": "expected 0 or more cells, got -1",
			},
		},
		{
			name: "a charset that is not a map is reported",
			wm:   map[string]any{"scroll_bar_charset": "▐"},
			wantError: map[string]string{
				"window_manager.scroll_bar_charset": "",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"browser": map[string]any{"window_manager": tc.wm},
			}, errors: map[string]error{}}

			wmCfg := cfg.windowManagerConfig()
			assert.Equal(t, tc.want, wmCfg.ScrollBarCharSet)
			assert.Equal(t, tc.wantHover, wmCfg.ScrollBarHoverCharSet)
			assert.Equal(t, tc.wantOutset, wmCfg.ScrollBarOutset)
			assert.Len(t, cfg.errors, len(tc.wantError))
			for key, msg := range tc.wantError {
				if assert.Contains(t, cfg.errors, key) {
					assert.ErrorContains(t, cfg.errors[key], msg)
				}
			}
		})
	}
}

func TestConfigWindowBarGraphics(t *testing.T) {
	def := handler.DefaultWindowManagerConfig().WindowBarCharSet
	cases := []struct {
		name      string
		charset   map[string]any
		want      tcomponent.WindowBarCharSet
		wantError map[string]string
	}{
		{
			name:    "runes alone draw no images",
			charset: map[string]any{"horizontal": "▀"},
			want:    tcomponent.WindowBarCharSet{Left: def.Left, Horizontal: '▀', Right: def.Right},
		},
		{
			name: "every slot",
			charset: map[string]any{"graphics": map[string]any{
				"mode":       "overlay",
				"left":       "file:///bar/left.png",
				"horizontal": "https://example.com/bar/middle.png",
				"right":      "file:///bar/right.png",
			}},
			want: tcomponent.WindowBarCharSet{
				Left: def.Left, Horizontal: def.Horizontal, Right: def.Right,
				Graphics: tcomponent.WindowBarGraphics{
					Left:       "file:///bar/left.png",
					Horizontal: "https://example.com/bar/middle.png",
					Right:      "file:///bar/right.png",
					Overlay:    true,
				},
			},
		},
		{
			name: "invalid graphics keep the runes",
			charset: map[string]any{
				"left":     "▛",
				"graphics": map[string]any{"left": "file:///bar/left.png", "right": "/bar/right.png"},
			},
			want: tcomponent.WindowBarCharSet{Left: '▛', Horizontal: def.Horizontal, Right: def.Right},
			wantError: map[string]string{
				"window_manager.window_bar_charset": `graphics.right: imageuri: unsupported scheme ""`,
			},
		},
		{
			name: "an outset stretches the ends",
			charset: map[string]any{"graphics": map[string]any{
				"left": "file:///bar/left.png",
				"outset": map[string]any{
					"top": 1, "bottom": 2.0, "sides": 4, "inward": 3, "horizontal_top": 1,
				},
			}},
			want: tcomponent.WindowBarCharSet{
				Left: def.Left, Horizontal: def.Horizontal, Right: def.Right,
				Graphics: tcomponent.WindowBarGraphics{
					Left: "file:///bar/left.png",
					Outset: tcomponent.WindowBarOutset{
						Top: 1, Bottom: 2, Sides: 4, Inward: 3, HorizontalTop: 1,
					},
				},
			},
		},
		{
			name: "invalid outset sides are reported and skipped",
			charset: map[string]any{"graphics": map[string]any{
				"left": "file:///bar/left.png",
				"outset": map[string]any{
					"top": -1, "bottom": "one", "sides": 2, "inward": -2, "horizontal_top": -3,
				},
			}},
			want: tcomponent.WindowBarCharSet{
				Left: def.Left, Horizontal: def.Horizontal, Right: def.Right,
				Graphics: tcomponent.WindowBarGraphics{
					Left:   "file:///bar/left.png",
					Outset: tcomponent.WindowBarOutset{Sides: 2},
				},
			},
			wantError: map[string]string{
				"window_manager.window_bar_charset.graphics.outset.top":            "expected 0 or more cells, got -1",
				"window_manager.window_bar_charset.graphics.outset.bottom":         config.ErrInvalidType.Error(),
				"window_manager.window_bar_charset.graphics.outset.inward":         "expected 0 or more cells, got -2",
				"window_manager.window_bar_charset.graphics.outset.horizontal_top": "expected 0 or more cells, got -3",
			},
		},
		{
			name: "an outset that is not a map is reported",
			charset: map[string]any{"graphics": map[string]any{
				"left": "file:///bar/left.png", "outset": 1,
			}},
			want: tcomponent.WindowBarCharSet{
				Left: def.Left, Horizontal: def.Horizontal, Right: def.Right,
				Graphics: tcomponent.WindowBarGraphics{Left: "file:///bar/left.png"},
			},
			wantError: map[string]string{
				"window_manager.window_bar_charset.graphics.outset": config.ErrInvalidType.Error(),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &ideConfig{cfg: map[string]any{
				"browser": map[string]any{"window_manager": map[string]any{
					"window_bar_charset": tc.charset,
				}},
			}, errors: map[string]error{}}

			assert.Equal(t, tc.want, cfg.windowManagerConfig().WindowBarCharSet)
			assert.Len(t, cfg.errors, len(tc.wantError))
			for key, msg := range tc.wantError {
				if assert.Contains(t, cfg.errors, key) {
					assert.ErrorContains(t, cfg.errors[key], msg)
				}
			}
		})
	}
}

func TestLoadEmbededConfig(t *testing.T) {
	var cfg ideConfig
	err := loadConfig(&cfg, "nonExistent", nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
	got := cfg.tutorialFiles("/data")
	require.Equal(t, 2, len(got))
	assert.Equal(t, "/etc/x.star", got["basics"])
	assert.Equal(t, "/etc/y.star", got["advanced"])
	assert.Empty(t, cfg.errors)
}

func TestTutorialFilesExpandDataDir(t *testing.T) {
	cfg := ideConfig{
		cfg: map[string]any{
			"tutorials": map[string]any{
				"pkg":   "$RUNE_DATADIR/pkg/fuzzy_search/1/fuzzy_search.star",
				"other": "$HOME/x.star",
			},
		},
		errors: map[string]error{},
	}
	assert.Equal(t, map[string]string{
		"pkg":   "/data/pkg/fuzzy_search/1/fuzzy_search.star",
		"other": "$HOME/x.star",
	}, cfg.tutorialFiles("/data"))
}

func TestTutorialFilesMissingReturnsNil(t *testing.T) {
	cfg := ideConfig{
		cfg:    map[string]any{},
		errors: map[string]error{},
	}
	assert.Nil(t, cfg.tutorialFiles("/data"))
	assert.Empty(t, cfg.errors)
}

func TestTutorialFilesWrongRootTypeRecordsError(t *testing.T) {
	cfg := ideConfig{
		cfg: map[string]any{
			"tutorials": "not a map",
		},
		errors: map[string]error{},
	}
	assert.Nil(t, cfg.tutorialFiles("/data"))
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
	got := cfg.tutorialFiles("/data")
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
	require.NoError(t, err)
	assert.Empty(t, cfg.consolePrompt())
}

func TestShellEditorModalFromEditorMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		editor map[string]any
		want   bool
	}{
		{"unset editor defaults modal", nil, true},
		{"vim", map[string]any{"mode": "vim"}, true},
		{"deprecated modal", map[string]any{"mode": "modal"}, true},
		{"modeless", map[string]any{"mode": "modeless"}, false},
		{"exo fallback vim", map[string]any{
			"mode": "exo",
			"exo":  map[string]any{"command": "vim {file}", "fallback": "vim"},
		}, true},
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
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
			err = loadConfig(&cfg, f.Name(), nil,
				DefaultConfig{src: "config = {}"},
				sounds.Silent, term.ScheduleNextTick)
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
	err = loadConfig(&cfg, f.Name(), nil,
		DefaultConfig{src: "config = {}"},
		sounds.Silent, term.ScheduleNextTick)
	require.NoError(t, err)
	assert.Equal(t, text.IndentConfig{
		"yaml": text.IndentRuneSpace,
		"go":   text.IndentRuneTab,
	}, cfg.editorIndents())
}

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

func quickMenuSpriteEntry(title, command string, sprite any) map[string]any {
	return map[string]any{"title": title, "command": command, "sprite": sprite}
}

func TestQuickMenuButtons(t *testing.T) {
	t.Parallel()
	const (
		idle    = "file:///hud/idle.png"
		hover   = "file:///hud/hover.png"
		pressed = "https://example.com/pressed.png"
	)
	every := map[string]any{"idle": idle, "hover": hover, "pressed": pressed}
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
	}, {
		name:    "a sprite replaces the symbol",
		entries: []any{quickMenuSpriteEntry("Help", "help", every)},
		want: []QuickMenuButton{{Title: "Help", Command: []string{"help"},
			Sprite: QuickMenuSprite{Idle: idle, Hover: hover, Pressed: pressed}}},
	}, {
		name: "a symbol is kept alongside a sprite",
		entries: []any{map[string]any{
			"symbol": "plus", "command": "help", "sprite": every,
		}},
		want: []QuickMenuButton{{Symbol: "plus", Title: "help", Command: []string{"help"},
			Sprite: QuickMenuSprite{Idle: idle, Hover: hover, Pressed: pressed}}},
	}, {
		name: "hover defaults to idle and pressed to hover",
		entries: []any{
			quickMenuSpriteEntry("Idle", "help", map[string]any{"idle": idle}),
			quickMenuSpriteEntry("Hover", "config",
				map[string]any{"idle": idle, "hover": hover}),
		},
		want: []QuickMenuButton{
			{Title: "Idle", Command: []string{"help"},
				Sprite: QuickMenuSprite{Idle: idle, Hover: idle, Pressed: idle}},
			{Title: "Hover", Command: []string{"config"},
				Sprite: QuickMenuSprite{Idle: idle, Hover: hover, Pressed: hover}},
		},
	}, {
		name:     "a sprite needs an idle image",
		entries:  []any{quickMenuSpriteEntry("Help", "help", map[string]any{"hover": hover})},
		wantErrs: []string{"gui.quick_menu[0].sprite.idle"},
	}, {
		name:     "a sprite must be a dict",
		entries:  []any{quickMenuSpriteEntry("Help", "help", idle)},
		wantErrs: []string{"gui.quick_menu[0].sprite"},
	}, {
		name: "sprite images must be valid URIs",
		entries: []any{quickMenuSpriteEntry("Help", "help", map[string]any{
			"idle": "ftp://example.com/idle.png", "pressed": 3,
		})},
		wantErrs: []string{
			"gui.quick_menu[0].sprite.idle", "gui.quick_menu[0].sprite.pressed",
		},
	}, {
		name: "unknown sprite keys are rejected",
		entries: []any{quickMenuSpriteEntry("Help", "help",
			map[string]any{"idle": idle, "focus": hover})},
		wantErrs: []string{"gui.quick_menu[0].sprite"},
	}, {
		name: "once a button sets a sprite every button needs one",
		entries: []any{
			quickMenuSpriteEntry("Help", "help", every),
			quickMenuEntry("plus", "Open Project", "workspaceopen"),
			quickMenuSpriteEntry("Settings", "config", every),
		},
		want: []QuickMenuButton{
			{Title: "Help", Command: []string{"help"},
				Sprite: QuickMenuSprite{Idle: idle, Hover: hover, Pressed: pressed}},
			{Title: "Settings", Command: []string{"config"},
				Sprite: QuickMenuSprite{Idle: idle, Hover: hover, Pressed: pressed}},
		},
		wantErrs: []string{"gui.quick_menu[1].sprite"},
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
			assert.Len(t, errs, len(tc.wantErrs))
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

func TestQuickMenuSounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		cfg          map[string]any
		hover, click string
	}{
		{name: "unset plays none", cfg: map[string]any{}},
		{name: "no gui section plays none", cfg: map[string]any{"gui": map[string]any{}}},
		{
			name: "both events",
			cfg: map[string]any{"gui": map[string]any{"quick_menu_sounds": map[string]any{
				"hover": "tick", "click": "info",
			}}},
			hover: "tick", click: "info",
		},
		{
			name: "an event left out plays none",
			cfg: map[string]any{"gui": map[string]any{"quick_menu_sounds": map[string]any{
				"click": "success",
			}}},
			click: "success",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hover, click := QuickMenuSounds(config.MapConfig(tc.cfg))
			assert.Equal(t, tc.hover, hover)
			assert.Equal(t, tc.click, click)
		})
	}
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

func TestValidateQuickMenuKeepsSprites(t *testing.T) {
	t.Parallel()
	sprite := map[string]any{"idle": "file:///hud/help.png"}
	cfg := map[string]any{"gui": map[string]any{"quick_menu": []any{
		quickMenuSpriteEntry("Help", "help", sprite),
		quickMenuEntry("plus", "Open Project", "workspaceopen"),
	}}}

	err := validateQuickMenu(cfg)
	assert.ErrorContains(t, err, "gui.quick_menu[1].sprite")

	want := []QuickMenuButton{{Title: "Help", Command: []string{"help"},
		Sprite: QuickMenuSprite{Idle: "file:///hud/help.png",
			Hover: "file:///hud/help.png", Pressed: "file:///hud/help.png"}}}
	assert.Equal(t, want, QuickMenuButtons(config.MapConfig(cfg)))
	assert.NoError(t, validateQuickMenu(cfg), "the neutralised tree must be valid")
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

func TestMetaOpenURLConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		cfg           map[string]any
		wantCommand   string
		wantClipboard bool
		wantErr       bool
	}{
		{name: "unset", cfg: map[string]any{}},
		{name: "system", cfg: map[string]any{"meta_open_url": "system"}},
		{
			name:          "clipboard",
			cfg:           map[string]any{"meta_open_url": "clipboard"},
			wantClipboard: true,
		},
		{
			name:        "command",
			cfg:         map[string]any{"meta_open_url": "browser $URL"},
			wantCommand: "browser $URL",
		},
		{name: "blank", cfg: map[string]any{"meta_open_url": "  "}, wantErr: true},
		{name: "not a string", cfg: map[string]any{"meta_open_url": 1}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opened bool
			c := ideConfig{cfg: tc.cfg, errors: map[string]error{},
				systemOpenURL: func(*url.URL) error { opened = true; return nil }}

			got := c.metaOpenURL()

			assert.Equal(t, tc.wantCommand, got.command)
			assert.Equal(t, tc.wantClipboard, got.clipboard)
			require.NotNil(t, got.system)
			require.NoError(t, got.system(&url.URL{}))
			assert.True(t, opened, "the system opener must be the IDE's")
			if tc.wantErr {
				assert.Contains(t, c.errors, "meta_open_url")
			} else {
				assert.Empty(t, c.errors)
			}
		})
	}
}
