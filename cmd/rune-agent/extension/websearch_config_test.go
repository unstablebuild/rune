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

package extension

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"gopkg.in/yaml.v3"
)

func noEnv(string) string { return "" }

func TestWebSearchToolIsOffUnlessAProviderIsNamed(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
	}{
		{"no web_search block", map[string]any{}},
		{"no provider", map[string]any{"web_search": map[string]any{}}},
		{"empty provider", map[string]any{"web_search": map[string]any{"provider": ""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noti := &capturingNotifications{}
			assert.Nil(t, webSearchTool(config.MapConfig(tt.cfg), noti, noEnv))
			assert.Empty(t, noti.captured())
		})
	}
}

func TestWebSearchToolKeenable(t *testing.T) {
	var asked []string
	getenv := func(name string) string {
		asked = append(asked, name)
		return ""
	}
	tool := webSearchTool(config.MapConfig(map[string]any{
		"web_search": map[string]any{"provider": "keenable"},
	}), &capturingNotifications{}, getenv)
	require.NotNil(t, tool)
	assert.Equal(t, "web_search", tool.Definition().Function.Name)
	assert.Equal(t, []string{"KEENABLE_API_KEY"}, asked)
}

func TestWebSearchToolWarnsAboutUnknownProviders(t *testing.T) {
	noti := &capturingNotifications{}
	tool := webSearchTool(config.MapConfig(map[string]any{
		"web_search": map[string]any{"provider": "altavista"},
	}), noti, noEnv)
	assert.Nil(t, tool)
	notes := noti.captured()
	require.Len(t, notes, 1)
	assert.Equal(t, browserapi.LevelWarn, notes[0].level)
	assert.Contains(t, notes[0].msg, `"altavista"`)
}

func TestShippedConfigLeavesWebSearchOff(t *testing.T) {
	data, err := os.ReadFile(agentYAMLPath)
	require.NoError(t, err)
	var doc struct {
		Extensions struct {
			RuneAgent struct {
				Config map[string]any `yaml:"config"`
			} `yaml:"rune-agent"`
		} `yaml:"extensions"`
	}
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Nil(t, webSearchTool(
		config.MapConfig(doc.Extensions.RuneAgent.Config), &capturingNotifications{}, noEnv),
		"web_search stays off until the user names a provider")
}
