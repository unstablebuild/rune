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

package dialoguetui

import (
	"bytes"
	"context"
	stdimage "image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"gopkg.in/yaml.v3"
)

// agentYAMLPath is the config Rune Agent ships with.
const agentYAMLPath = "../../config.yaml"

func TestReadConfig(t *testing.T) {
	tests := []struct {
		name      string
		own, host map[string]any
		check     func(t *testing.T, got ComponentConfig)
	}{
		{
			name: "nothing configured",
			check: func(t *testing.T, got ComponentConfig) {
				def := DefaultComponentConfig()
				assert.Equal(t, term.ColorDefault, got.BackgroundColor)
				assert.Equal(t, term.ColorGray, got.SendMessageStringConfig.Bg)
				assert.Equal(t, def.SendMessageIcon, got.SendMessageIcon)
				assert.Equal(t, def.MessagesRowConfig, got.MessagesRowConfig)
				assert.Equal(t, DefaultPinnedMessageMaxRows, got.PinnedMessageMaxRows)
				assert.Equal(t, component.FrameCharSetDefault(), got.InputBox.FrameCharSet)
				assert.Equal(t, DefaultStatusBarConfig(), got.StatusBar)
				require.NotNil(t, got.Editor, "editor.mode defaults to vim")
				assert.True(t, got.EditorModal)
				assert.True(t, got.ModalStartInsert)
			},
		},
		{
			name: "every key",
			own: map[string]any{
				"background_attr":               map[string]any{"bg": "navy"},
				"user_msg_background_attr":      map[string]any{"bg": "teal"},
				"user_msg_attr":                 map[string]any{"fg": "yellow"},
				"assistant_msg_attr":            map[string]any{"fg": "lime"},
				"pinned_user_msg_max_lines":     4,
				"transcript_horizontal_padding": 10,
				"user_msg_icon":                 ">",
				"input_box_attr":                map[string]any{"fg": "white", "bg": "red"},
				"input_box_placeholder_attr":    map[string]any{"fg": "olive"},
				"input_box_frame_attr":          map[string]any{"fg": "aqua"},
				"input_box_frame_charset":       map[string]any{"topleft": "╔"},
				"completion_matched_text_attr":  map[string]any{"fg": "red"},
				"completion_focus_element_attr": map[string]any{"fg": "green"},
				"completion_element_attr":       map[string]any{"fg": "blue"},
				"completion_radar_color":        "fuchsia",
				"inline_attachment_attr":        map[string]any{"fg": "maroon"},
				"status_bar":                    map[string]any{"enabled": false},
				"editor_modal_start_insert":     false,
			},
			host: map[string]any{"editor": map[string]any{"mode": "standard"}},
			check: func(t *testing.T, got ComponentConfig) {
				assert.Equal(t, term.ColorNavy, got.BackgroundColor)
				assert.Equal(t, term.ColorNavy, got.ToolCallStringConfig.Bg)
				assert.Equal(t, term.ColorNavy, got.SelectionConfig.TitleStringConfig.Bg)
				assert.Equal(t, term.ColorTeal, got.SendMessageStringConfig.BackgroundAttributes.Bg)
				assert.Equal(t, term.Attributes{Fg: term.ColorYellow}, got.SendMessageStringConfig.Attributes)
				assert.Equal(t, term.Attributes{Fg: term.ColorLime}, got.ReceiveMessageStringConfig.Attributes)
				assert.Equal(t, 4, got.PinnedMessageMaxRows)
				assert.Equal(t, 10, got.MessagesRowConfig.PadHorizontal)
				assert.EqualValues(t, '>', got.SendMessageIcon)
				assert.Equal(t, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorRed}, got.InputBox.ContentAttr)
				assert.Equal(t, term.ColorRed, got.InputBackgroundColor)
				assert.Equal(t, term.Attributes{Fg: term.ColorOlive}, got.InputBox.PlaceholderConfig.Attributes)
				assert.Equal(t, term.Attributes{Fg: term.ColorAqua}, got.InputBox.FrameAttr)
				assert.EqualValues(t, '╔', got.InputBox.FrameCharSet.TopLeft)
				assert.Equal(t, term.Attributes{Fg: term.ColorRed}, got.CompletionMatchedTextAttr)
				assert.Equal(t, term.Attributes{Fg: term.ColorGreen}, got.CompletionFocusElementAttr)
				assert.Equal(t, term.Attributes{Fg: term.ColorBlue}, got.CompletionElementAttr)
				assert.Equal(t, term.ColorFuchsia, got.CompletionRadarColor)
				assert.Equal(t, term.Attributes{Fg: term.ColorMaroon}, got.InlineAttachmentAttr)
				assert.False(t, got.StatusBar.Enabled)
				require.NotNil(t, got.Editor)
				assert.False(t, got.EditorModal)
				assert.False(t, got.ModalStartInsert)
			},
		},
		{
			name: "an input background over the input box's",
			own: map[string]any{
				"input_box_attr":         map[string]any{"bg": "red"},
				"input_background_color": "blue",
			},
			check: func(t *testing.T, got ComponentConfig) {
				assert.Equal(t, term.ColorBlue, got.InputBackgroundColor)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clip := clipboard.NewInMemory()
			own, host := config.NopConfig(), config.NopConfig()
			if tt.own != nil {
				own = config.MapConfig(tt.own)
			}
			if tt.host != nil {
				host = config.MapConfig(tt.host)
			}
			got := ReadConfig(own, host, clip, nopNotifications{})
			assert.Equal(t, clip, got.Clipboard)
			tt.check(t, got)
		})
	}
}

func TestReadStyle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corner.png")
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, stdimage.NewRGBA(stdimage.Rect(0, 0, 1, 1))))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	corner := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	own := config.MapConfig(map[string]any{
		"status_bar": map[string]any{"layout": "{{ .Model }}"},
		"input_box_frame_charset": map[string]any{
			"topleft": "╓", "graphics": map[string]any{"topleft": corner},
		},
		"user_msg_icon": "",
	})
	barStorage, frameStorage := storagestub.NewInMemoryService(), storagestub.NewInMemoryService()
	cfg := DefaultComponentConfig()
	cfg.DurationPrecision = time.Second
	cfg.StatusBar.Storage = barStorage
	cfg.InputBox.GraphicsStorage = frameStorage

	got, frameImages := ReadStyle(context.Background(), cfg, own, config.NopConfig(), nopNotifications{})

	assert.Equal(t, []StatusBarComponent{{Type: StatusBarModel, Template: "%s"}}, got.StatusBar.Layout)
	assert.True(t, got.StatusBar.Enabled)
	assert.Equal(t, barStorage, got.StatusBar.Storage)
	assert.Equal(t, time.Second, got.StatusBar.DurationPrecision)
	assert.EqualValues(t, '╓', got.InputBox.FrameCharSet.TopLeft)
	assert.Equal(t, frameStorage, got.InputBox.GraphicsStorage)
	require.Contains(t, frameImages, corner)
	assert.Equal(t, cfg.SendMessageIcon, got.SendMessageIcon, "only the bar and the frame change")

	cfg.InputBox.GraphicsStorage = nil
	_, frameImages = ReadStyle(context.Background(), cfg, own, config.NopConfig(), nopNotifications{})
	assert.Nil(t, frameImages, "a frame without storage draws no images")
}

func TestResolveInputBackgroundColor(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		def  term.Color
		want term.Color
	}{
		{
			name: "no input_box_attr keeps the default",
			cfg:  map[string]any{},
			def:  term.ColorDefault,
			want: term.ColorDefault,
		},
		{
			name: "input_box_attr without bg keeps the default",
			cfg:  map[string]any{"input_box_attr": map[string]any{"fg": "default"}},
			def:  term.ColorDefault,
			want: term.ColorDefault,
		},
		{
			name: "input_box_attr bg default keeps the default",
			cfg: map[string]any{
				"input_box_attr": map[string]any{"fg": "default", "bg": "default"},
			},
			def:  term.ColorDefault,
			want: term.ColorDefault,
		},
		{
			name: "explicit bg overrides the default",
			cfg: map[string]any{
				"input_box_attr": map[string]any{"bg": "red"},
			},
			def:  term.ColorDefault,
			want: term.ColorRed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveInputBackgroundColor(tc.def, config.MapConfig(tc.cfg))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDefaultComponentInputBackgroundIsDefault(t *testing.T) {
	assert.Equal(t, term.ColorDefault, DefaultComponentConfig().InputBackgroundColor,
		"compose input must default to the terminal default background")
}

func TestResolveUserMsgIcon(t *testing.T) {
	const def = '→'
	tests := []struct {
		name string
		cfg  map[string]any
		want rune
	}{
		{name: "unset keeps the default", cfg: map[string]any{}, want: def},
		{name: "a character replaces it", cfg: map[string]any{"user_msg_icon": "\uf054"}, want: '\uf054'},
		{name: "empty hides it", cfg: map[string]any{"user_msg_icon": ""}, want: 0},
		{name: "several characters keep the default", cfg: map[string]any{"user_msg_icon": ">>"}, want: def},
		{name: "a non-string keeps the default", cfg: map[string]any{"user_msg_icon": true}, want: def},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveUserMsgIcon(def, config.MapConfig(tc.cfg)))
		})
	}
}

func TestShippedAgentConfigUserMsgIconMatchesDefault(t *testing.T) {
	raw, err := os.ReadFile(agentYAMLPath)
	require.NoError(t, err)
	var doc struct {
		Extensions struct {
			RuneAgent struct {
				Config map[string]any `yaml:"config"`
			} `yaml:"rune-agent"`
		} `yaml:"extensions"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	pconfig := config.MapConfig(doc.Extensions.RuneAgent.Config)

	_, err = pconfig.GetString("user_msg_icon")
	require.NoError(t, err, "user_msg_icon missing from %s", agentYAMLPath)
	assert.Equal(t, DefaultSendMessageIcon, DefaultComponentConfig().SendMessageIcon)
	assert.Equal(t, DefaultComponentConfig().SendMessageIcon, resolveUserMsgIcon(0, pconfig),
		"user_msg_icon in %s", agentYAMLPath)
}

func TestResolveTranscriptHorizontalPadding(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		want int
	}{
		{name: "unset keeps 90 columns", cfg: map[string]any{}, want: -90},
		{name: "a negative value is the width", cfg: map[string]any{"transcript_horizontal_padding": -120}, want: -120},
		{name: "a positive value is the padding", cfg: map[string]any{"transcript_horizontal_padding": 20}, want: 20},
		{name: "zero fills the tab", cfg: map[string]any{"transcript_horizontal_padding": 0}, want: 0},
		{name: "a non-integer keeps 90 columns", cfg: map[string]any{"transcript_horizontal_padding": "wide"}, want: -90},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveTranscriptHorizontalPadding(config.MapConfig(tc.cfg)))
		})
	}
}

func TestTranscriptHorizontalPaddingLaysOutTheTranscript(t *testing.T) {
	const width, height = 100, 10
	tests := []struct {
		name    string
		padding int
		// wantX is the column the transcript starts at.
		wantX int
	}{
		{name: "a width centers the transcript", padding: -90, wantX: 5},
		{name: "a narrower width centers the transcript", padding: -80, wantX: 10},
		{name: "a width wider than the tab fills it", padding: -120},
		{name: "a padding splits evenly", padding: 20, wantX: 10},
		{name: "no padding fills the tab", padding: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultComponentConfig()
			cfg.MessagesRowConfig.PadHorizontal = tc.padding
			comp := NewComponent(cfg)
			t.Cleanup(func() { assert.NoError(t, comp.Close()) })
			comp.Resize(width, height)
			comp.AddWarningMessage("x")
			w := term.NewStringWriter(width, height)
			comp.Draw(w)
			require.NoError(t, w.Flush())
			x := -1
			for i, c := range w.Cells() {
				if c.Ch == 'x' {
					x = i % width
					break
				}
			}
			require.GreaterOrEqual(t, x, 0, "the message is drawn")
			assert.Equal(t, tc.wantX, x)
		})
	}
}

func TestShippedAgentConfigTranscriptAndInputBoxFrameMatchDefaults(t *testing.T) {
	raw, err := os.ReadFile(agentYAMLPath)
	require.NoError(t, err)
	var doc struct {
		Extensions struct {
			RuneAgent struct {
				Config map[string]any `yaml:"config"`
			} `yaml:"rune-agent"`
		} `yaml:"extensions"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	pconfig := config.MapConfig(doc.Extensions.RuneAgent.Config)

	_, err = pconfig.GetInt("transcript_horizontal_padding")
	require.NoError(t, err, "transcript_horizontal_padding missing from %s", agentYAMLPath)
	assert.Equal(t, DefaultComponentConfig().MessagesRowConfig.PadHorizontal,
		resolveTranscriptHorizontalPadding(pconfig), "transcript_horizontal_padding in %s", agentYAMLPath)

	_, err = pconfig.GetConfig("input_box_frame_charset")
	require.NoError(t, err, "input_box_frame_charset missing from %s", agentYAMLPath)
	assert.Equal(t, component.FrameCharSetDefault(), inputBoxFrameCharSet(pconfig),
		"input_box_frame_charset in %s", agentYAMLPath)
}
